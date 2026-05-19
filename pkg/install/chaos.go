package install

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2" // nolint
	"github.com/stretchr/testify/require"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/gruntwork-io/terratest/modules/helm"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
)

// InstallChaosMesh installs the Chaos Mesh Helm chart into the specified namespace.
//
// The function uses the provided Helm chart path and values file to perform a Helm upgrade
// (which will create the release if it doesn't exist). It also applies an additional
// manifest to install ebtables on the node and waits until the Chaos Mesh controller
// deployment becomes available.
//
// Parameters:
// - ctx: parent context for the installation operation (not used for propagation into Helm here).
// - helmChart: path or name of the Helm chart to install/upgrade.
// - valuesFile: path to the Helm values file to apply.
// - t: terratest testing interface for running commands and assertions.
// - namespace: Kubernetes namespace in which to install the chart.
// - releaseName: the Helm release name to use for the upgrade.
func InstallChaosMesh(ctx context.Context, helmChart, valuesFile string, t terratesting.TestingT, namespace string, releaseName string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	helmOpts := &helm.Options{
		KubectlOptions: kubeOpts,
		ValuesFiles:    []string{valuesFile},
		ExtraArgs: map[string][]string{
			"upgrade": {"--create-namespace", "--wait", "--timeout", "10m"},
		},
	}

	By(fmt.Sprintf("Install %s chart", helmChart))
	err := helm.UpgradeE(t, helmOpts, helmChart, releaseName)
	if err != nil {
		t.Fatalf("Failed to install chart %s: %v", helmChart, err)
	}

	// Install ebtables on the node
	manifestPath := consts.ManifestsRoot() + "/chaos-mesh-operator/ebtables.yaml"
	KubectlApply(ctx, t, kubeOpts, manifestPath)

	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "chaos-controller-manager", consts.Retries, consts.PollingInterval)
}

// ApplyChaosScenario applies a Chaos Mesh scenario manifest without waiting for completion.
//
// The scenario manifest is loaded from the provided scenario folder and filename, then
// namespaced references inside the manifest are replaced with the provided namespace.
// The manifest is applied and control returns immediately; the scenario runs autonomously
// via Chaos Mesh. Use RunChaosScenario if blocking until completion is required.
//
// Parameters:
// - ctx: context for the kubectl apply operation.
// - t: terratest testing interface for running commands and assertions.
// - namespace: Kubernetes namespace where the scenario should be executed.
// - scenarioFolder: folder under manifests/chaos-tests that contains the scenario.
// - scenario: filename (without extension) of the scenario to run.
func ApplyChaosScenario(ctx context.Context, t terratesting.TestingT, namespace, scenarioFolder, scenario string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)

	manifestPath := fmt.Sprintf("%s/chaos-tests/%s/%s.yaml", consts.ManifestsRoot(), scenarioFolder, scenario)
	manifestContent, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	updatedManifestContent := strings.ReplaceAll(string(manifestContent), "- vm", fmt.Sprintf("- %s", namespace))
	updatedManifestContent = strings.ReplaceAll(updatedManifestContent, "vmstorage-vm-", fmt.Sprintf("vmstorage-%s-", namespace))

	KubectlApplyFromString(ctx, t, kubeOpts, updatedManifestContent)
}

// RunChaosScenario applies a Chaos Mesh scenario manifest and waits for it to complete.
//
// The scenario manifest is loaded from the provided scenario folder and filename, then
// namespaced references inside the manifest (for example "- vm") are replaced with the
// provided namespace. After applying the modified manifest, this function waits for the
// scenario resource to reach a terminal state (using WaitForChaosScenarioToComplete).
//
// Parameters:
// - ctx: context used for waiting for scenario completion.
// - t: terratest testing interface for running commands and assertions.
// - namespace: Kubernetes namespace where the scenario should be executed.
// - scenarioFolder: folder under manifests/chaos-tests that contains the scenario.
// - scenario: filename (without extension) of the scenario to run.
// - chaosType: the resource type for the chaos scenario (e.g., "podchaos", "networkchaos").
func RunChaosScenario(ctx context.Context, t terratesting.TestingT, namespace, scenarioFolder, scenario, chaosType string) {
	kubeOpts := k8s.NewKubectlOptions("", "", namespace)

	kubeConfigPath, err := kubeOpts.GetConfigPath(t)
	require.NoError(t, err)
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigPath}, &clientcmd.ConfigOverrides{})
	restConfig, err := clientConfig.ClientConfig()
	require.NoError(t, err)

	dynamicClient := dynamic.NewForConfigOrDie(restConfig)
	require.NoError(t, err)

	ApplyChaosScenario(ctx, t, namespace, scenarioFolder, scenario)

	By("Waiting for chaos scenario to complete")
	WaitForChaosScenarioToComplete(ctx, t, dynamicClient, namespace, scenario, chaosType)
}

// WaitForChaosScenarioToComplete waits for a Chaos Mesh scenario resource to reach completion.
//
// This function polls the dynamic API for the given chaos resource (identified by group/version/resource)
// and checks the resource's status for a condition indicating recovery (type "AllRecovered" with status "True").
// It will also handle the case where the resource is deleted. An independent timeout
// (consts.ChaosTestMaxDuration) is enforced even if ctx is cancelled; ctx.Done() triggers a graceful
// return without failing, so callers can cancel and still let the chaos scenario drain.
//
// Parameters:
// - ctx: context used to signal graceful shutdown; cancellation does NOT count as a timeout failure.
// - t: terratest testing interface for running assertions (used to fail the test on timeouts/errors).
// - chaosClient: dynamic Kubernetes client for interacting with Chaos Mesh custom resources.
// - namespace: Kubernetes namespace where the chaos resource lives.
// - scenario: name of the chaos resource (the object name).
// - chaosType: resource type name under the chaos-mesh.org group (e.g., "podchaos", "networkchaos").
func WaitForChaosScenarioToComplete(ctx context.Context, t terratesting.TestingT, chaosClient *dynamic.DynamicClient, namespace, scenario, chaosType string) {
	gvr := schema.GroupVersionResource{Group: "chaos-mesh.org", Version: "v1alpha1", Resource: chaosType}

	chaosTestOverCtx, cancel := context.WithTimeout(context.Background(), consts.ChaosTestMaxDuration)
	defer cancel()

	ticker := time.NewTicker(consts.PollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-chaosTestOverCtx.Done():
			t.Fatalf("timed out waiting for chaos scenario %s to finish", scenario)
		case <-ticker.C:
			obj, err := chaosClient.Resource(gvr).Namespace(namespace).Get(chaosTestOverCtx, scenario, metav1.GetOptions{})
			if err != nil {
				if k8serrors.IsNotFound(err) {
					continue
				}
				t.Fatalf("failed to get chaos scenario %s: %v", scenario, err)
			}
			status, found, err := unstructured.NestedMap(obj.Object, "status")
			if err != nil || !found {
				continue
			}
			conditions, found, err := unstructured.NestedSlice(status, "conditions")
			if err != nil || !found {
				continue
			}
			for _, condition := range conditions {
				if condition == nil {
					continue
				}
				conditionMap, ok := condition.(map[string]interface{})
				if !ok {
					continue
				}
				condType := conditionMap["type"]
				condStatus := conditionMap["status"]
				if condStatus == "True" && (condType == "AllRecovered" || condType == "WorkflowAccomplished") {
					return
				}
			}
		}
	}
}
