package install

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	k6v1alpha1 "github.com/grafana/k6-operator/api/v1alpha1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
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
	k8s.KubectlApply(t, kubeOpts, "../../manifests/k6-operator/bundle.yaml")
	k8s.WaitUntilDeploymentAvailable(t, kubeOpts, "k6-operator-controller-manager", consts.Retries, consts.PollingInterval)
}

// RunK6Scenario creates the required k6 resources for running a load test scenario.
//
// This function reads a JavaScript scenario file from manifests/load-tests, replaces
// a hardcoded vmselect URL pattern with a dynamically computed VMSelect service
// address for the target namespace, and creates a ConfigMap containing the
// scenario script. It then creates a k6-operator TestRun custom resource that
// references that ConfigMap and triggers the test run. The function waits for
// the initializer and starter jobs to complete before returning.
//
// k6 metrics are exported via the Prometheus remote write output to the Overwatch
// VMSingle instance, allowing k6 performance data to be stored in the monitoring
// stack alongside VictoriaMetrics health metrics.
//
// Parameters:
// - ctx: parent context used for waiting operations (not currently used for cancellation).
// - t: terratest testing interface used for applying manifests and assertions.
// - k6namespace: namespace where k6-operator and associated TestRun/ConfigMap should be created.
// - targetNamespace: namespace of the VictoriaMetrics deployment that is the target of the test.
// - scenario: base name of the scenario file (without .js extension).
// - parallelism: number of k6 parallel instances to request for the TestRun.
// Returns an error if reading or marshaling manifests fails.
func RunK6Scenario(ctx context.Context, t terratesting.TestingT, k6namespace, targetNamespace, scenario string, parallelism int) error {
	kubeOpts := k8s.NewKubectlOptions("", "", k6namespace)

	scenarioPath := fmt.Sprintf("../../manifests/load-tests/%s.js", scenario)
	scenarioContent, err := os.ReadFile(scenarioPath)
	if err != nil {
		return fmt.Errorf("failed to read scenario file: %w", err)
	}

	// Replace URL placeholders with addresses derived from the target namespace.
	replacements := []struct{ old, new string }{
		{
			`const VMSELECT_URL = "http://vmselect-vmks.monitoring.svc.cluster.local:8481/select/0/prometheus/api/v1/query_range"`,
			fmt.Sprintf(`const VMSELECT_URL = "http://%s/select/0/prometheus/api/v1/query_range"`,
				consts.GetVMSelectSvc(consts.DefaultReleaseName, targetNamespace)),
		},
		{
			`const VMINSERT_URL = "http://vminsert-vmks.monitoring.svc.cluster.local:8480/insert/0/prometheus/api/v1/import/prometheus"`,
			fmt.Sprintf(`const VMINSERT_URL = "http://%s/insert/0/prometheus/api/v1/import/prometheus"`,
				consts.GetVMInsertSvc(consts.DefaultReleaseName, targetNamespace)),
		},
	}
	updatedScenarioContent := string(scenarioContent)
	for _, r := range replacements {
		updatedScenarioContent = strings.ReplaceAll(updatedScenarioContent, r.old, r.new)
	}

	// Create a configmap with a script
	configMap := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      scenario,
			Namespace: k6namespace,
		},
		Data: map[string]string{
			"script.js": updatedScenarioContent,
		},
	}
	yamlConfigMap, err := yaml.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("failed to marshal configMap: %w", err)
	}
	k8s.KubectlApplyFromString(t, kubeOpts, string(yamlConfigMap))

	// Create TestRun CR
	testRun := &k6v1alpha1.TestRun{
		TypeMeta: metav1.TypeMeta{
			Kind:       "TestRun",
			APIVersion: "k6.io/v1alpha1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      scenario,
			Namespace: k6namespace,
		},
		Spec: k6v1alpha1.TestRunSpec{
			Script: k6v1alpha1.K6Script{
				ConfigMap: k6v1alpha1.K6Configmap{
					Name: scenario,
					File: "script.js",
				},
			},
			Parallelism: int32(parallelism),
			Arguments:   "--out experimental-prometheus-rw",
			Runner: k6v1alpha1.Pod{
				Env: []corev1.EnvVar{
					{
						Name:  "K6_PROMETHEUS_RW_SERVER_URL",
						Value: fmt.Sprintf("http://%s/prometheus/api/v1/write", consts.GetVMSingleSvc("overwatch", consts.OverwatchNamespace)),
					},
				},
			},
		},
	}
	yamlTestRun, err := yaml.Marshal(testRun)
	if err != nil {
		return fmt.Errorf("failed to marshal testRun: %w", err)
	}
	k8s.KubectlApplyFromString(t, kubeOpts, string(yamlTestRun))

	k8s.WaitUntilJobSucceed(t, kubeOpts, fmt.Sprintf("%s-initializer", scenario), consts.Retries, consts.PollingInterval)
	k8s.WaitUntilJobSucceed(t, kubeOpts, fmt.Sprintf("%s-starter", scenario), consts.Retries, consts.PollingInterval)
	return nil
}

// WaitForK6JobsToComplete waits for all parallel k6 jobs for the given scenario to finish.
//
// The function polls Kubernetes Jobs created by the k6 operator using the naming
// pattern "<scenario>-<index>". It waits for each job up to K6Retries with a
// polling interval defined by K6JobPollingInterval. The function uses terratest
// helpers to perform the waits and will fail the test if any job does not
// succeed within the timeout.
//
// Parameters:
// - ctx: parent context for waiting (not currently used for cancellation).
// - t: terratest testing interface used for assertions and waits.
// - namespace: Kubernetes namespace where the k6 jobs are executed.
// - scenario: base name of the scenario whose jobs should be waited on.
// - parallelism: number of parallel job instances to wait for.
func WaitForK6JobsToComplete(ctx context.Context, t terratesting.TestingT, namespace, scenario string, parallelism int) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)

	for idx := 0; idx < parallelism; idx++ {
		k8s.WaitUntilJobSucceed(t, kubeOpts, fmt.Sprintf("%s-%d", scenario, idx+1), consts.K6Retries, consts.K6JobPollingInterval)
	}
}
