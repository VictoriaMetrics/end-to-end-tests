package install

import (
	"os"

	. "github.com/onsi/ginkgo/v2" //nolint

	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

// InstallVMGather provisions the VMGather deployment used by some end-to-end tests.
//
// Behavior:
//   - Ensures the `vmgather` namespace exists.
//   - Reads the VMGather manifest from the repository manifests.
//   - Patches the ingress host in-memory using JSON patch to match the test environment.
//   - Applies the modified manifest and waits for the `vmgather` deployment to become available.
//
// Parameters:
// - t: terratest testing interface used to perform kubectl operations and assertions.
func InstallVMGather(t terratesting.TestingT) {
	namespace := "vmgather"

	kubeOpts := k8s.NewKubectlOptions("", "", namespace)
	if _, err := k8s.GetNamespaceE(t, kubeOpts, namespace); err != nil {
		k8s.CreateNamespace(t, kubeOpts, namespace)
	}

	By("Install VMGather")
	vmgatherYaml, err := os.ReadFile(consts.ManifestsRoot() + "/vmgather.yaml")
	require.NoError(t, err)
	k8s.KubectlApplyFromString(t, kubeOpts, string(vmgatherYaml))

	// Patch the ingress host in-memory
	vmgatherYaml, err = os.ReadFile(consts.ManifestsRoot() + "/vmgather-ingress.yaml")
	require.NoError(t, err)
	patchOps := []PatchOp{
		{
			Op:    "replace",
			Path:  "/spec/rules/0/host",
			Value: consts.VMGatherHost(),
		},
	}
	patch, err := CreateJsonPatch(patchOps)
	require.NoError(t, err)
	vmgatherJson, err := yaml.YAMLToJSON(vmgatherYaml)
	require.NoError(t, err)
	vmgatherJson, err = patch.Apply(vmgatherJson)
	require.NoError(t, err)
	vmgatherPatched, err := yaml.JSONToYAML(vmgatherJson)
	require.NoError(t, err)
	k8s.KubectlApplyFromString(t, kubeOpts, string(vmgatherPatched))

	By("Wait for vmgather deployment to be available")
	k8s.WaitUntilDeploymentAvailable(t, kubeOpts, "vmgather", consts.Retries, consts.PollingInterval)
}
