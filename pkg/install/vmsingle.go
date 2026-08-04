package install

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"time"

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

func patchAndApplyVMSingleManifest(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, vmsingleYamlPath string, jsonPatches []jsonpatch.Patch) {
	ensureLicenseSecret(t, kubeOpts, namespace)
	jsonPatches = appendLicensePatch(t, jsonPatches)

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
	helpers.Logf("Installing VMSingle in namespace %s", namespace)
	KubectlApplyFromString(ctx, t, kubeOpts, string(vmsingleJson))
}

// InstallVMSingle installs a single-node VictoriaMetrics instance (VMSingle) into the specified namespace.
//
// It performs the following steps:
// 1. Ensures the target namespace exists.
// 2. Reads the VMSingle manifest from "../../manifests/components/vmsingle.yaml".
// 3. Applies the manifest using kubectl.
// 4. Waits for the VMSingle instance to become operational.
// 5. Exposes the VMSingle instance via an Ingress.
func InstallVMSingle(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, jsonPatches []jsonpatch.Patch, operationalTimeout time.Duration) {
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}

	patchAndApplyVMSingleManifest(ctx, t, kubeOpts, namespace, consts.ManifestsRoot()+"/components/vmsingle.yaml", jsonPatches)

	// Wait for VMSingle to become operational
	WaitForVMSingleToBeOperational(ctx, t, kubeOpts, namespace, vmclient, operationalTimeout)

	k8s.WaitUntilDeploymentAvailableContext(t, ctx, kubeOpts, "vmsingle-vmsingle", consts.Retries, consts.PollingInterval)

	// Expose VMSingle as ingress
	ExposeVMSingleAsIngress(ctx, t, kubeOpts, namespace)
}

// ExposeVMSingleAsIngress creates an Ingress resource to expose the VMSingle instance.
//
// It reads the ingress template from "../../manifests/overwatch/vmsingle-ingress.yaml",
// replaces the host placeholder with the configured VMSingle host, and applies it.
func ExposeVMSingleAsIngress(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	vmsingleYaml, err := os.ReadFile(consts.OverwatchVMSingleIngress())
	require.NoError(t, err)

	docJson, err := yaml.YAMLToJSON(vmsingleYaml)
	require.NoError(t, err)

	host := consts.VMSingleNamespacedHost(namespace)

	patches := []string{
		fmt.Sprintf(`[{"op": "replace", "path": "/spec/rules/0/host", "value": "%s"}]`, host),
		fmt.Sprintf(`[{"op": "add", "path": "/metadata/namespace", "value": "%s"}]`, namespace),
		`[{"op": "replace", "path": "/spec/rules/0/http/paths/0/backend/service/name", "value": "vmsingle-vmsingle"}]`,
	}

	for _, patch := range patches {
		patchObj, err := jsonpatch.DecodePatch([]byte(patch))
		require.NoError(t, err)
		docJson, err = patchObj.Apply(docJson)
		require.NoError(t, err)
	}

	KubectlApplyFromString(ctx, t, kubeOpts, string(docJson))
	readyURL := fmt.Sprintf("http://%s%s/api/v1/query?query=%s", host, consts.PrometheusPathSuffix, url.QueryEscape("1"))
	WaitForHTTPRoute(ctx, t, readyURL)
}

// WaitForVMSingleToBeOperational polls a VMSingle custom resource until it reports an operational status.
//
// "actual pod count: 0 less than needed" is transient: the operator may report it during
// initial PVC provisioning (WaitForFirstConsumer storage class) before the pod is created.
func WaitForVMSingleToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, timeout time.Duration) {
	waitForOperational(ctx, t, kubeOpts, timeout, "VMSingle", namespace, func(fctx context.Context) ([]resourceStatus, error) {
		list, err := vmclient.OperatorV1beta1().VMSingles(namespace).List(fctx, metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		result := make([]resourceStatus, len(list.Items))
		for i := range list.Items {
			result[i] = resourceStatus{Name: list.Items[i].Name, Status: list.Items[i].Status.UpdateStatus, Reason: list.Items[i].Status.Reason}
		}
		return result, nil
	}, "actual pod count: 0 less than needed")
}

// DeleteVMSingle deletes the specified VMSingle resource from the cluster.
// It ignores "not found" errors.
func DeleteVMSingle(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmsingleName string) {
	// Delete the VMSingle resource
	helpers.Logf("Deleting VMSingle %s", vmsingleName)
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "vmsingle", vmsingleName, "--ignore-not-found=true")
}
