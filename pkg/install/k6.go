package install

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	k6v1alpha1 "github.com/grafana/k6-operator/api/v1alpha1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

// InstallK6 installs the k6-operator into the given namespace.
//
// The function applies the bundled operator manifests located under
// manifests/k6-operator/bundle.yaml and waits for the operator controller
// deployment to become available. The provided terratest testing interface is
// used for applying manifests and waiting for readiness.
//
// Parameters:
// - ctx: parent context for the operation (currently not used for cancellation).
// - t: terratest testing interface used for running commands and assertions.
// - namespace: Kubernetes namespace in which to install the k6 operator.
func InstallK6(ctx context.Context, t terratesting.TestingT, namespace string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	KubectlApply(ctx, t, kubeOpts, consts.ManifestsRoot()+"/k6-operator/bundle.yaml")
	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "k6-operator-controller-manager", consts.Retries, consts.PollingInterval)
}

// RunK6Scenario creates a ConfigMap and TestRun CR in namespace to run a load test scenario.
//
// This function reads a JavaScript scenario file from manifests/load-tests, replaces
// hardcoded URL and namespace placeholders with dynamically computed values for the
// target VMCluster, and creates a ConfigMap containing the scenario script. It then
// creates a k6-operator TestRun custom resource that references that ConfigMap and
// triggers the test run. The function waits for the initializer and starter jobs to
// complete before returning.
//
// k6 metrics are exported via the Prometheus remote write output to the Overwatch
// VMSingle instance, allowing k6 performance data to be stored in the monitoring
// stack alongside VictoriaMetrics health metrics.
//
// Parameters:
// - ctx: parent context used for waiting operations (not currently used for cancellation).
// - t: terratest testing interface used for applying manifests and assertions.
// - namespace: namespace of the VictoriaMetrics deployment; TestRun and ConfigMap are created here.
// - clusterName: name of the VMCluster resource within namespace.
// - scenario: base name of the scenario file (without .js extension).
// - parallelism: number of k6 parallel instances to request for the TestRun.
// Returns an error if reading or marshaling manifests fails.
func RunK6Scenario(ctx context.Context, t terratesting.TestingT, namespace, clusterName, scenario string, parallelism int, scenarioName string, extraEnvVars map[string]string) error {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	vmselectSvc := consts.GetVMSelectSvc(clusterName, namespace)
	vminsertSvc := consts.GetVMInsertSvc(clusterName, namespace)
	vmselectURL := fmt.Sprintf("http://%s/select/0/prometheus/api/v1/query_range", vmselectSvc)
	vminsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/import/prometheus", vminsertSvc)
	vminsertOTLPURL := fmt.Sprintf("http://%s/insert/0/opentelemetry/v1/metrics", vminsertSvc)

	deleteSeriesURL := fmt.Sprintf(
		"http://%s/delete_series?%s",
		vmselectSvc,
		url.Values{
			"match[]": []string{`{__name__!=""}`},
			"end":     []string{fmt.Sprintf("%d", time.Now().Unix())},
		}.Encode(),
	)
	client := &http.Client{Timeout: consts.HTTPClientTimeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deleteSeriesURL, nil)
	if err != nil {
		helpers.Logf("WARNING: failed to build delete_series request: %v", err)
	} else {
		resp, err := client.Do(req)
		if err != nil {
			helpers.Logf("WARNING: delete_series request failed: %v", err)
		} else {
			resp.Body.Close()
			helpers.Logf("Deleted all series before k6 scenario start (status %d)", resp.StatusCode)
		}
	}

	scenarioPath := fmt.Sprintf("%s/load-tests/%s.js", consts.ManifestsRoot(), scenario)
	scenarioContent, err := os.ReadFile(scenarioPath)
	if err != nil {
		return fmt.Errorf("failed to read scenario file: %w", err)
	}

	// Replace URL and namespace placeholders with values derived from the target cluster.
	replacements := []struct{ old, new string }{
		{
			`const VMSELECT_URL = "http://vmselect-vmks.monitoring.svc.cluster.local:8481/select/0/prometheus/api/v1/query_range"`,
			fmt.Sprintf(`const VMSELECT_URL = %q`, vmselectURL),
		},
		{
			`const VMINSERT_URL = "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/import/prometheus";`,
			fmt.Sprintf(`const VMINSERT_URL = %q;`, vminsertURL),
		},
		{
			`const VMINSERT_OTLP_URL = "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/opentelemetry/v1/metrics";`,
			fmt.Sprintf(`const VMINSERT_OTLP_URL = %q;`, vminsertOTLPURL),
		},
		{
			`const VM_NAMESPACE = "monitoring";`,
			fmt.Sprintf(`const VM_NAMESPACE = %q;`, namespace),
		},
	}
	updatedScenarioContent := string(scenarioContent)
	for _, r := range replacements {
		updatedScenarioContent = strings.ReplaceAll(updatedScenarioContent, r.old, r.new)
	}

	envVars := []corev1.EnvVar{
		{
			Name:  "K6_PROMETHEUS_RW_SERVER_URL",
			Value: fmt.Sprintf("http://%s/prometheus/api/v1/write", consts.GetVMSingleSvc(consts.DefaultReleaseName, consts.DefaultVMNamespace)),
		},
		{
			Name:  "K6_PROMETHEUS_RW_TREND_STATS",
			Value: "p(95),p(99),min,max",
		},
		{
			Name:  "VMSELECT_URL",
			Value: vmselectURL,
		},
		{
			Name:  "VMINSERT_URL",
			Value: vminsertURL,
		},
		{
			Name:  "VMINSERT_OTLP_URL",
			Value: vminsertOTLPURL,
		},
		{
			Name:  "VM_NAMESPACE",
			Value: namespace,
		},
	}

	for k, v := range extraEnvVars {
		found := false
		for i, envVar := range envVars {
			if envVar.Name == k {
				envVars[i].Value = v
				found = true
				break
			}
		}
		if !found {
			envVars = append(envVars, corev1.EnvVar{Name: k, Value: v})
		}
	}
	waitForK6BackendsReady(ctx, t, kubeOpts, scenarioName,
		k6BackendHealthURL(k6EnvValue(envVars, "VMSELECT_URL")),
		k6BackendHealthURL(k6EnvValue(envVars, "VMINSERT_URL")),
	)

	configMap := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      scenarioName,
			Namespace: namespace,
		},
		Data: map[string]string{
			"script.js": updatedScenarioContent,
		},
	}
	yamlConfigMap, err := yaml.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("failed to marshal configMap: %w", err)
	}
	KubectlApplyFromString(ctx, t, kubeOpts, string(yamlConfigMap))

	// Create TestRun CR
	testRun := &k6v1alpha1.TestRun{
		TypeMeta: metav1.TypeMeta{
			Kind:       "TestRun",
			APIVersion: "k6.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      scenarioName,
			Namespace: namespace,
		},
		Spec: k6v1alpha1.TestRunSpec{
			Script: k6v1alpha1.K6Script{
				ConfigMap: k6v1alpha1.K6Configmap{
					Name: scenarioName,
					File: "script.js",
				},
			},
			Parallelism: int32(parallelism),
			Arguments:   "--out experimental-prometheus-rw --tag job=k6",
			Runner: k6v1alpha1.Pod{
				Env:       envVars,
				Resources: k6RunnerResources(),
			},
		},
	}
	yamlTestRun, err := yaml.Marshal(testRun)
	if err != nil {
		return fmt.Errorf("failed to marshal testRun: %w", err)
	}
	KubectlApplyFromString(ctx, t, kubeOpts, string(yamlTestRun))

	k8s.WaitUntilJobSucceedContext(t, ctx, kubeOpts, fmt.Sprintf("%s-initializer", scenarioName), consts.Retries, consts.PollingInterval)
	k8s.WaitUntilJobSucceedContext(t, ctx, kubeOpts, fmt.Sprintf("%s-starter", scenarioName), consts.Retries, consts.PollingInterval)
	return nil
}

func k6EnvValue(envVars []corev1.EnvVar, name string) string {
	for _, envVar := range envVars {
		if envVar.Name == name {
			return envVar.Value
		}
	}
	return ""
}

func k6BackendHealthURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.Path = "/health"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func waitForK6BackendsReady(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, scenarioName, vmselectHealthURL, vminsertHealthURL string) {
	if vmselectHealthURL == "" || vminsertHealthURL == "" {
		return
	}

	jobName := fmt.Sprintf("%s-backend-ready", scenarioName)
	checkScript := fmt.Sprintf(`for i in $(seq 1 60); do
  curl -fsS --max-time 5 %s && curl -fsS --max-time 5 %s && exit 0
  sleep 5
done
exit 1`, shellQuote(vmselectHealthURL), shellQuote(vminsertHealthURL))

	k8s.RunKubectlContext(t, ctx, kubeOpts, "delete", "job", jobName, "--ignore-not-found=true")
	defer k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "job", jobName, "--ignore-not-found=true")

	k8s.RunKubectlContext(t, ctx, kubeOpts,
		"create", "job", jobName,
		"--image=curlimages/curl:8.16.0",
		"--", "sh", "-c", checkScript,
	)
	k8s.WaitUntilJobSucceedContext(t, ctx, kubeOpts, jobName, consts.Retries, consts.PollingInterval)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func k6RunnerResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{},
	}
}

// WaitForK6JobsToComplete waits for all parallel k6 jobs for the given scenario to finish.
//
// The function polls Kubernetes Jobs created by the k6 operator using the naming
// pattern "<scenario>-<index>". It waits for all jobs within K6JobMaxDuration.
// The function uses terratest helpers to perform the waits and will fail the test
// if any job does not succeed within the timeout.
//
// Parameters:
// - ctx: parent context for waiting (not currently used for cancellation).
// - t: terratest testing interface used for assertions and waits.
// - namespace: Kubernetes namespace where the k6 jobs are executed.
// - scenario: base name of the scenario whose jobs should be waited on.
// - parallelism: number of parallel job instances to wait for.
func WaitForK6JobsToComplete(ctx context.Context, t terratesting.TestingT, namespace, scenarioName string, parallelism int) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	ctx, cancel := context.WithTimeout(ctx, consts.K6JobMaxDuration)
	defer cancel()

	for idx := 0; idx < parallelism; idx++ {
		k8s.WaitUntilJobSucceedContext(t, ctx, kubeOpts, fmt.Sprintf("%s-%d", scenarioName, idx+1), consts.K6Retries, consts.K6JobPollingInterval)
	}
}
