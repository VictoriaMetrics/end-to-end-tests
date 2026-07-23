package install

import (
	"context"
	"os"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	watchtools "k8s.io/client-go/tools/watch"
)

// InstallVMAuth installs a VMAuth instance into the specified namespace.
//
// Parameters:
// - ctx: context for cancellation and timeouts.
// - t: terratest testing interface.
// - kubeOpts: Kubernetes options including namespace.
// - namespace: target Kubernetes namespace.
// - vmc: VictoriaMetrics operator client.
// - jsonPatches: list of json patches to apply to the VMAuth resource.
func InstallVMAuth(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmc vmclient.Interface, jsonPatches []jsonpatch.Patch) {
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
	}

	vmAuthYaml, err := os.ReadFile(consts.ManifestsRoot() + "/vmauth.yaml")
	require.NoError(t, err, "failed to read VMAuth YAML")

	vmAuthJSON, err := yaml.YAMLToJSON(vmAuthYaml)
	require.NoError(t, err, "failed to convert VMAuth YAML to JSON")

	for _, patch := range jsonPatches {
		vmAuthJSON, err = patch.Apply(vmAuthJSON)
		require.NoError(t, err, "failed to apply patch")
	}

	helpers.Logf("Installing VMAuth in namespace %s", namespace)
	KubectlApplyFromString(ctx, t, kubeOpts, string(vmAuthJSON))

	WaitForVMAuthToBeOperational(ctx, t, kubeOpts, namespace, vmc)
}

// WaitForVMAuthToBeOperational watches a VMAuth custom resource until it reports an operational status.
func WaitForVMAuthToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmc vmclient.Interface) {
	watchInterface, err := vmc.OperatorV1beta1().VMAuths(namespace).Watch(ctx, metav1.ListOptions{})
	require.NoError(t, err)
	defer watchInterface.Stop()

	timeBoundContext, cancel := context.WithTimeout(ctx, consts.ResourceWaitTimeout)
	defer cancel()

	_, err = watchtools.UntilWithoutRetry(timeBoundContext, watchInterface, func(event watch.Event) (bool, error) {
		vmAuth, ok := event.Object.(*vmv1beta1.VMAuth)
		if !ok {
			return false, nil
		}
		return vmAuth.Status.UpdateStatus == vmv1beta1.UpdateStatusOperational, nil
	})
	require.NoError(t, err)
}

// DeleteVMAuth deletes the specified VMAuth resource from the cluster.
// It ignores "not found" errors.
func DeleteVMAuth(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmAuthName string) {
	helpers.Logf("Deleting VMAuth %s", vmAuthName)
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "vmauth", vmAuthName, "--ignore-not-found=true")
}
