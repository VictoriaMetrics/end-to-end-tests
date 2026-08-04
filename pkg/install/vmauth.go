package install

import (
	"context"
	"os"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/VictoriaMetrics/end-to-end-tests/pkg/helpers"
	vmclient "github.com/VictoriaMetrics/operator/api/client/versioned"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InstallVMAuth installs a VMAuth instance into the specified namespace.
func InstallVMAuth(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmc vmclient.Interface, jsonPatches []jsonpatch.Patch) {
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
	}

	vmAuthYaml, err := os.ReadFile(consts.ManifestsRoot() + "/components/vmauth.yaml")
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

// WaitForVMAuthToBeOperational polls a VMAuth custom resource until it reports an operational status.
func WaitForVMAuthToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmc vmclient.Interface) {
	waitForOperational(ctx, t, kubeOpts, consts.ResourceWaitTimeout, "VMAuth", namespace, func(fctx context.Context) ([]resourceStatus, error) {
		list, err := vmc.OperatorV1beta1().VMAuths(namespace).List(fctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		result := make([]resourceStatus, len(list.Items))
		for i := range list.Items {
			result[i] = resourceStatus{Name: list.Items[i].Name, Status: list.Items[i].Status.UpdateStatus, Reason: list.Items[i].Status.Reason}
		}
		return result, nil
	})
}

// DeleteVMAuth deletes the specified VMAuth resource from the cluster.
// It ignores "not found" errors.
func DeleteVMAuth(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmAuthName string) {
	helpers.Logf("Deleting VMAuth %s", vmAuthName)
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "vmauth", vmAuthName, "--ignore-not-found=true")
}
