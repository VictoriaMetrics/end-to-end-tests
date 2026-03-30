package install

import (
	"context"
	"fmt"
	"os"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
)

func patchAndApplyVMSingleManifest(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, vmsingleYamlPath string, jsonPatches []jsonpatch.Patch) {
	if consts.LicenseFile() != "" {
		secretYaml, err := consts.PrepareLicenseSecret(namespace)
		require.NoError(t, err)

		k8s.KubectlApplyFromString(t, kubeOpts, secretYaml)

		patchJSON := fmt.Sprintf(`[{"op": "add", "path": "/spec/license", "value": {"keyRef": {"name": "%s", "key": "%s"}}}]`, consts.LicenseSecretName, consts.LicenseSecretKey)
		patch, err := jsonpatch.DecodePatch([]byte(patchJSON))
		require.NoError(t, err)
		jsonPatches = append(jsonPatches, patch)
	}

	// Read VMSingle manifest and patch it
	vmsingleYaml, err := os.ReadFile(vmsingleYamlPath)
	require.NoError(t, err, "failed to read VMSingle YAML")

	vmsingleJson, err := yaml.YAMLToJSON(vmsingleYaml)
	require.NoError(t, err, "failed to convert VMSingle YAML to JSON")

	for _, patch := range jsonPatches {
		vmsingleJson, err = patch.Apply(vmsingleJson)
		require.NoError(t, err, "failed to apply patch")
	}

	// Apply the VMSingle manifest
	fmt.Printf("Installing VMSingle in namespace %s\n", namespace)
	k8s.KubectlApplyFromString(t, kubeOpts, string(vmsingleJson))
}

// InstallVMSingle installs a single-node VictoriaMetrics instance (VMSingle) into the specified namespace.
//
// It performs the following steps:
// 1. Ensures the target namespace exists.
// 2. Reads the VMSingle manifest from "../../manifests/vmsingle.yaml".
// 3. Applies the manifest using kubectl.
// 4. Waits for the VMSingle instance to become operational.
// 5. Exposes the VMSingle instance via an Ingress.
//
// Parameters:
// - ctx: context for cancellation and timeouts.
// - t: terratest testing interface.
// - kubeOpts: Kubernetes options including namespace.
// - namespace: target Kubernetes namespace.
// - vmclient: VictoriaMetrics operator client.
// - jsonPatches: list of json patches to apply to the VMSingle resource.
func InstallVMSingle(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, jsonPatches []jsonpatch.Patch) {
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceE(t, kubeOpts, namespace); err != nil {
		k8s.CreateNamespace(t, kubeOpts, namespace)
	}

	patchAndApplyVMSingleManifest(ctx, t, kubeOpts, namespace, consts.ManifestsRoot()+"/vmsingle.yaml", jsonPatches)

	// Wait for VMSingle to become operational
	WaitForVMSingleToBeOperational(ctx, t, kubeOpts, namespace, vmclient)

	k8s.WaitUntilDeploymentAvailable(t, kubeOpts, "vmsingle-vmsingle", consts.Retries, consts.PollingInterval)

	// Expose VMSingle as ingress
	ExposeVMSingleAsIngress(ctx, t, kubeOpts, namespace)
}

// ExposeVMSingleAsIngress creates an Ingress resource to expose the VMSingle instance.
//
// It reads the ingress template from "../../manifests/overwatch/vmsingle-ingress.yaml",
// replaces the host placeholder with the configured VMSingle host, and applies it.
//
// Parameters:
// - ctx: context for the operation.
// - t: terratest testing interface.
// - kubeOpts: Kubernetes options.
// - namespace: Kubernetes namespace where the ingress should be created.
func ExposeVMSingleAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	// Copy vmsingle-ingress.yaml to temp file, update ingress host and apply it
	vmsingleYaml, err := os.ReadFile(consts.OverwatchVMSingleIngress())
	require.NoError(t, err)

	docJson, err := yaml.YAMLToJSON(vmsingleYaml)
	require.NoError(t, err)

	host := consts.VMSingleHost()
	if namespace != "overwatch" {
		host = consts.VMSingleNamespacedHost(namespace)
	}

	patches := []string{
		fmt.Sprintf(`[{"op": "replace", "path": "/spec/rules/0/host", "value": "%s"}]`, host),
		fmt.Sprintf(`[{"op": "add", "path": "/metadata/namespace", "value": "%s"}]`, namespace),
	}

	if namespace != "overwatch" {
		patches = append(patches, `[{"op": "replace", "path": "/spec/rules/0/http/paths/0/backend/service/name", "value": "vmsingle-vmsingle"}]`)
	}

	for _, patch := range patches {
		patchObj, err := jsonpatch.DecodePatch([]byte(patch))
		require.NoError(t, err)
		docJson, err = patchObj.Apply(docJson)
		require.NoError(t, err)
	}

	k8s.KubectlApplyFromString(t, kubeOpts, string(docJson))
	k8s.WaitUntilIngressAvailable(t, kubeOpts, "vmsingle-ingress", consts.Retries, consts.PollingInterval)
}

// WaitForVMSingleToBeOperational watches a VMSingle custom resource until it reports an operational status.
//
// The function sets up a watch for VMSingle objects in the provided namespace and
// blocks until the VMSingle's Status.UpdateStatus becomes UpdateStatusOperational or
// the wait times out. It uses consts.ResourceWaitTimeout to bound the wait.
func WaitForVMSingleToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface) {
	watchInterface, err := vmclient.OperatorV1beta1().VMSingles(namespace).Watch(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	defer watchInterface.Stop()

	timeBoundContext, cancel := context.WithTimeout(ctx, consts.ResourceWaitTimeout)
	defer cancel()

	_, err = watchtools.UntilWithoutRetry(timeBoundContext, watchInterface, func(event watch.Event) (bool, error) {
		obj := event.Object
		vmSingle := obj.(*vmv1beta1.VMSingle)
		if vmSingle.Status.UpdateStatus == vmv1beta1.UpdateStatusOperational {
			return true, nil
		}
		return false, nil
	})
	require.NoError(t, err)
}

// DeleteVMSingle deletes the specified VMSingle resource from the cluster.
// It ignores "not found" errors.
//
// Parameters:
// - t: terratest testing interface.
// - kubeOpts: Kubernetes options.
// - vmsingleName: name of the VMSingle resource to delete.
func DeleteVMSingle(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmsingleName string) {
	// Delete the VMSingle resource
	fmt.Printf("Deleting VMSingle %s\n", vmsingleName)
	k8s.RunKubectl(t, kubeOpts, "delete", "vmsingle", vmsingleName, "--ignore-not-found=true")
}
