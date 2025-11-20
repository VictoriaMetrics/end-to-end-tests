package install

import (
	"context"
	"fmt"
	"os"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"

	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
	"k8s.io/client-go/util/retry"
)

// InstallVMAgent installs a single-node VictoriaMetrics instance (VMAgent) into the specified namespace.
//
// It performs the following steps:
// 1. Ensures the target namespace exists.
// 2. Reads the VMAgent manifest from "../../manifests/vmagent.yaml".
// 3. Applies the manifest using kubectl.
// 4. Waits for the VMAgent instance to become operational.
//
// Parameters:
// - ctx: context for cancellation and timeouts.
// - t: terratest testing interface.
// - kubeOpts: Kubernetes options including namespace.
// - namespace: target Kubernetes namespace.
// - vmclient: VictoriaMetrics operator client.
// - jsonPatches: list of json patches to apply to the VMAgent resource.
func InstallVMAgent(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, jsonPatches []jsonpatch.Patch) {
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceE(t, kubeOpts, namespace); err != nil {
		k8s.CreateNamespace(t, kubeOpts, namespace)
		k8s.RunKubectl(t, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}

	// Read VMAgent and patch it
	vmagentYamlPath := consts.ManifestsRoot() + "/vmagent.yaml"
	vmagentYaml, err := os.ReadFile(vmagentYamlPath)
	require.NoError(t, err, "failed to read VMAgent YAML")

	vmagentJson, err := yaml.YAMLToJSON(vmagentYaml)
	require.NoError(t, err, "failed to convert VMAgent YAML to JSON")

	for _, patch := range jsonPatches {
		vmagentJson, err = patch.Apply(vmagentJson)
		require.NoError(t, err, "failed to apply patch")
	}

	// Apply the VMAgent manifest
	fmt.Printf("Installing VMAgent in namespace %s\n", namespace)
	k8s.KubectlApplyFromString(t, kubeOpts, string(vmagentJson))

	// Wait for VMAgent to become operational
	WaitForVMAgentToBeOperational(ctx, t, kubeOpts, namespace, vmclient)

	// Expose VMAgent as ingress
	ExposeVMAgentAsIngress(ctx, t, kubeOpts, namespace)
}

// ExposeVMAgentAsIngress creates an Ingress resource to expose the VMAgent instance.
//
// It reads the ingress template from "../../manifests/overwatch/vmsingle-ingress.yaml",
// replaces the host placeholder with the configured VMAgent host, and applies it.
//
// Parameters:
// - ctx: context for the operation.
// - t: terratest testing interface.
// - kubeOpts: Kubernetes options.
// - namespace: Kubernetes namespace where the ingress should be created.
func ExposeVMAgentAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	// Copy vmsingle-ingress.yaml to temp file, update ingress host and apply it
	vmagentYaml, err := os.ReadFile(consts.OverwatchVMSingleIngress())
	require.NoError(t, err)

	docJson, err := yaml.YAMLToJSON(vmagentYaml)
	require.NoError(t, err)

	host := consts.VMAgentNamespacedHost(namespace)

	patchOps := []PatchOp{
		{
			Op:    "replace",
			Path:  "/metadata/name",
			Value: "vmagent-ingress",
		},
		{
			Op:    "add",
			Path:  "/metadata/namespace",
			Value: namespace,
		},
		{
			Op:    "replace",
			Path:  "/spec/rules/0/host",
			Value: host,
		},
		{
			Op:    "replace",
			Path:  "/spec/rules/0/http/paths/0/backend/service/name",
			Value: "vmagent-vmagent",
		},
	}

	patchObj, err := CreateJsonPatch(patchOps)
	require.NoError(t, err)
	docJson, err = patchObj.Apply(docJson)
	require.NoError(t, err)

	k8s.KubectlApplyFromString(t, kubeOpts, string(docJson))
	k8s.WaitUntilIngressAvailable(t, kubeOpts, "vmagent-ingress", consts.Retries, consts.PollingInterval)
}

// EnsureVMAgentRemoteWriteURL ensures that the specified VMAgent contains a remoteWrite
// entry with the provided URL. If no remoteWrite entries exist or the provided URL is
// not present, the function appends a remoteWrite entry with that URL and updates the
// VMAgent resource in Kubernetes.
//
// This helper is intended for use in end-to-end tests to guarantee that a VMAgent is
// configured to forward data to a particular remote endpoint (for example, a VMSingle
// instance used in overwatch tests).
//
// Parameters:
//   - ctx: context for the API requests and potential cancellation.
//   - t: terratest testing interface used for assertions and error reporting.
//   - vmclient: client for interacting with VictoriaMetrics Operator CRDs.
//   - kubeOpts: terratest kubectl options referring to the cluster and namespace (not used
//     directly for API calls here but kept for symmetry with other helpers).
//   - namespace: Kubernetes namespace where the VMAgent CR lives.
//   - vmAgentName: name of the VMAgent custom resource to inspect and potentially update.
//   - url: the remoteWrite URL that must be present in the VMAgent configuration.
func EnsureVMAgentRemoteWriteURL(ctx context.Context, t terratesting.TestingT, vmclient vmclient.Interface, kubeOpts *k8s.KubectlOptions, namespace, vmAgentName, url string) {
	// Get the VMAgent resource
	vmAgent, err := vmclient.OperatorV1beta1().VMAgents(namespace).Get(ctx, vmAgentName, metav1.GetOptions{})
	require.NoError(t, err)

	// Check if remoteWrite is configured and has at least one URL
	if vmAgent == nil || len(vmAgent.Spec.RemoteWrite) == 0 {
		t.Errorf("VMAgent %s in namespace %s does not have any remoteWrite configuration", vmAgentName, namespace)
		return
	}

	// Validate that at least one remoteWrite entry has a URL
	found := false
	for _, rw := range vmAgent.Spec.RemoteWrite {
		if rw.URL == url {
			found = true
			break
		}
	}
	if !found {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			// Get the fresh VMAgent resource version as it may have been updated by another test
			vmAgent, err := vmclient.OperatorV1beta1().VMAgents(namespace).Get(ctx, vmAgentName, metav1.GetOptions{})
			if err != nil {
				return err
			}

			// Check again if the URL is already present to avoid duplicates during retries
			for _, rw := range vmAgent.Spec.RemoteWrite {
				if rw.URL == url {
					return nil
				}
			}

			vmAgent.Spec.RemoteWrite = append(vmAgent.Spec.RemoteWrite, vmv1beta1.VMAgentRemoteWriteSpec{
				URL: url,
			})
			_, err = vmclient.OperatorV1beta1().VMAgents(namespace).Update(ctx, vmAgent, metav1.UpdateOptions{})
			return err
		})
		require.NoError(t, err)
		WaitForVMAgentToBeOperational(ctx, t, kubeOpts, namespace, vmclient)
	}
}

// WaitForVMAgentToBeOperational watches the VMAgent custom resource in the given
// namespace and blocks until the agent reports an operational update status.
//
// The function uses a watch on VMAgent objects and a bounded timeout derived from
// consts.ResourceWaitTimeout. It returns by calling test assertions on the provided
// terratest testing interface if an error occurs during the wait.
//
// Parameters:
//   - ctx: parent context used for the watch and timeout propagation.
//   - t: terratest testing interface used for assertions and failing the test on errors.
//   - kubeOpts: terratest KubectlOptions pointing at the cluster/namespace (not used by the
//     watch but included for consistency with other helpers).
//   - namespace: the Kubernetes namespace where the VMAgent CR is located.
//   - vmclient: client for interacting with VictoriaMetrics Operator CRDs.
func WaitForVMAgentToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface) {
	watchInterface, err := vmclient.OperatorV1beta1().VMAgents(namespace).Watch(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	defer watchInterface.Stop()

	timeBoundContext, cancel := context.WithTimeout(ctx, consts.ResourceWaitTimeout)
	defer cancel()

	_, err = watchtools.UntilWithoutRetry(timeBoundContext, watchInterface, func(event watch.Event) (bool, error) {
		obj := event.Object
		vmAgent := obj.(*vmv1beta1.VMAgent)
		if vmAgent.Status.UpdateStatus == vmv1beta1.UpdateStatusOperational {
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err)
}

// DeleteVMAgent deletes the specified VMAgent resource from the cluster.
// It ignores "not found" errors.
//
// Parameters:
// - t: terratest testing interface.
// - kubeOpts: Kubernetes options.
// - vmagentName: name of the VMAgent resource to delete.
func DeleteVMAgent(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmagentName string) {
	// Delete the VMAgent resource
	fmt.Printf("Deleting VMAgent %s\n", vmagentName)
	k8s.RunKubectl(t, kubeOpts, "delete", "vmagent", vmagentName, "--ignore-not-found=true")
}
