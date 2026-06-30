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
	"github.com/gruntwork-io/terratest/modules/logger"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/yaml"

	k6v1alpha1 "github.com/grafana/k6-operator/api/v1alpha1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
)

// k6RunnerImage is a custom k6 v2 build that includes the xk6-client-prometheus-remote extension.
// Built with: xk6 build v2.0.0 --with github.com/grafana/xk6-client-prometheus-remote@v0.5.0
// See manifests/k6-runner/Dockerfile for build instructions.
const k6RunnerImage = "quay.io/vrutkovs/k6-with-prw-extension:v2.0.0"

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
	k8s.RunKubectlContext(t, ctx, kubeOpts, "delete", "testrun,configmap", scenarioName, "--ignore-not-found=true", "--wait=true")

	vmselectSvc := consts.GetVMSelectSvc(clusterName, namespace)
	vminsertSvc := consts.GetVMInsertSvc(clusterName, namespace)
	vmselectURL := fmt.Sprintf("http://%s/select/0/prometheus/api/v1/query_range", vmselectSvc)
	vminsertURL := fmt.Sprintf("http://%s/insert/0/prometheus/api/v1/write", vminsertSvc)
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

	updatedScenarioContent := string(scenarioContent)

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
			// k6 v2 can produce empty inspect output for archived scripts, which makes
			// k6-operator stop before creating starter and runner jobs.
			Initializer: &k6v1alpha1.Pod{Disabled: true},
			Runner:      k6RunnerPod(envVars),
		},
	}
	yamlTestRun, err := yaml.Marshal(testRun)
	if err != nil {
		return fmt.Errorf("failed to marshal testRun: %w", err)
	}
	KubectlApplyFromString(ctx, t, kubeOpts, string(yamlTestRun))

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
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("2"),
			corev1.ResourceMemory: resource.MustParse("256Mi"),
		},
	}
}

func k6RunnerPod(envVars []corev1.EnvVar) k6v1alpha1.Pod {
	return k6v1alpha1.Pod{
		Image:     k6RunnerImage,
		Env:       envVars,
		Resources: k6RunnerResources(),
	}
}

// testRunGVR is the GroupVersionResource for the k6-operator TestRun CRD.
var testRunGVR = schema.GroupVersionResource{
	Group:    "k6.io",
	Version:  "v1alpha1",
	Resource: "testruns",
}

// WaitForK6JobsToComplete watches the TestRun CR until it reaches a terminal stage.
//
// The k6 operator stage lifecycle: initialization → initialized → created →
// started → stopped (all runners done) → finished. "error" is the failure
// terminal. "stopped" is a brief intermediate — always followed by "finished"
// in the next reconcile — so we wait for "finished" specifically.
//
// Parameters:
// - ctx: parent context for waiting.
// - t: terratest testing interface used for assertions.
// - namespace: Kubernetes namespace where the TestRun lives.
// - scenarioName: name of the TestRun CR (and the k6 scenario).
// - parallelism: kept for API compatibility; not used.
// - maxDuration: maximum time to wait; zero uses consts.K6JobMaxDuration.
func WaitForK6JobsToComplete(ctx context.Context, t terratesting.TestingT, namespace, scenarioName string, parallelism int, maxDuration time.Duration) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	if maxDuration == 0 {
		maxDuration = consts.K6JobMaxDuration
	}
	ctx, cancel := context.WithTimeout(ctx, maxDuration)
	defer cancel()

	dynClient := GetDynamicClient(t, kubeOpts)
	ri := dynClient.Resource(testRunGVR).Namespace(namespace)

	// Initial check — the TestRun may already be in a terminal stage.
	if unstr, err := ri.Get(ctx, scenarioName, metav1.GetOptions{}); err == nil {
		if stage := testRunStageFromUnstructured(unstr.Object); isTerminalStage(stage) {
			handleTerminalStage(t, namespace, scenarioName, stage)
			return
		}
	}

	watcher, err := ri.Watch(ctx, metav1.ListOptions{
		FieldSelector: fields.OneTermEqualSelector("metadata.name", scenarioName).String(),
	})
	if err != nil {
		t.Fatal(fmt.Sprintf("k6 TestRun %s/%s: failed to start watch: %v", namespace, scenarioName, err))
		return
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			stage := ""
			if unstr, err := ri.Get(ctx, scenarioName, metav1.GetOptions{}); err == nil {
				stage = testRunStageFromUnstructured(unstr.Object)
			}
			t.Fatal(fmt.Sprintf("k6 TestRun %s/%s did not finish within timeout (stage=%q): %v",
				namespace, scenarioName, stage, ctx.Err()))
			return
		case event, ok := <-watcher.ResultChan():
			if !ok {
				// Watch channel closed — restart.
				watcher.Stop()
				watcher, err = ri.Watch(ctx, metav1.ListOptions{
					FieldSelector: fields.OneTermEqualSelector("metadata.name", scenarioName).String(),
				})
				if err != nil {
					t.Fatal(fmt.Sprintf("k6 TestRun %s/%s: failed to restart watch: %v", namespace, scenarioName, err))
					return
				}
				continue
			}
			if event.Type != watch.Modified && event.Type != watch.Added {
				continue
			}
			unstr, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			stage := testRunStageFromUnstructured(unstr.Object)
			if stage != "" {
				logger.Log(t, fmt.Sprintf("k6 TestRun %s stage: %q", scenarioName, stage))
			}
			if isTerminalStage(stage) {
				handleTerminalStage(t, namespace, scenarioName, stage)
				return
			}
		}
	}
}

func testRunStageFromUnstructured(obj map[string]interface{}) string {
	status, ok := obj["status"].(map[string]interface{})
	if !ok {
		return ""
	}
	stage, _ := status["stage"].(string)
	return stage
}

func isTerminalStage(stage string) bool {
	return stage == "finished" || stage == "error"
}

func handleTerminalStage(t terratesting.TestingT, namespace, scenarioName, stage string) {
	if stage == "finished" {
		logger.Log(t, fmt.Sprintf("k6 TestRun %s/%s finished", namespace, scenarioName))
		return
	}
	t.Fatal(fmt.Sprintf("k6 TestRun %s/%s reached terminal stage %q", namespace, scenarioName, stage))
}
