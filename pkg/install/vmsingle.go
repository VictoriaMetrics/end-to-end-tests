package install

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

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
)

func patchAndApplyVMSingleManifest(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace, vmsingleYamlPath string, jsonPatches []jsonpatch.Patch) {
	ensureVMSingleLicenseSecret(t, kubeOpts, namespace)
	jsonPatches = appendVMSingleLicensePatch(t, jsonPatches)

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

func vmsingleLicensePatch() (jsonpatch.Patch, error) {
	patchJSON := fmt.Sprintf(`[{"op": "add", "path": "/spec/license", "value": {"keyRef": {"name": "%s", "key": "%s"}}}]`, consts.LicenseSecretName, consts.LicenseSecretKey)
	return jsonpatch.DecodePatch([]byte(patchJSON))
}

func appendVMSingleLicensePatch(t terratesting.TestingT, jsonPatches []jsonpatch.Patch) []jsonpatch.Patch {
	if consts.LicenseFile() == "" {
		return jsonPatches
	}

	patch, err := vmsingleLicensePatch()
	require.NoError(t, err)
	return append(jsonPatches, patch)
}

func ensureVMSingleLicenseSecret(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	if consts.LicenseFile() == "" {
		return
	}

	secretYaml, err := consts.PrepareLicenseSecret(namespace)
	require.NoError(t, err)

	// Avoid KubectlApplyFromString wrapper here; it logs manifest contents.
	k8s.KubectlApplyFromStringContext(t, context.Background(), kubeOpts, secretYaml)
}

// InstallVMSingle installs a single-node VictoriaMetrics instance (VMSingle) into the specified namespace.
//
// It performs the following steps:
// 1. Ensures the target namespace exists.
// 2. Reads the VMSingle manifest from "../../manifests/vmsingle.yaml".
// 3. Applies the manifest using kubectl.
// 4. Waits for the VMSingle instance to become operational.
// 5. Exposes the VMSingle instance via an Ingress.
func InstallVMSingle(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, jsonPatches []jsonpatch.Patch, operationalTimeout time.Duration) {
	// Make sure namespace exists
	if _, err := k8s.GetNamespaceContextE(t, ctx, kubeOpts, namespace); err != nil {
		k8s.CreateNamespaceContext(t, ctx, kubeOpts, namespace)
		k8s.RunKubectlContext(t, ctx, kubeOpts, "label", "namespace", namespace, "goldilocks.fairwinds.com/enabled=true", "--overwrite")
	}

	patchAndApplyVMSingleManifest(ctx, t, kubeOpts, namespace, consts.ManifestsRoot()+"/vmsingle.yaml", jsonPatches)

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
	waitForVMSingleIngressRoute(ctx, t, namespace)
}

func waitForVMSingleIngressRoute(ctx context.Context, t terratesting.TestingT, namespace string) {
	host := consts.VMSingleNamespacedHost(namespace)

	readyURL := fmt.Sprintf("http://%s%s/api/v1/query?query=%s", host, consts.PrometheusPathSuffix, url.QueryEscape("1"))
	client := &http.Client{Timeout: consts.HTTPClientTimeout}
	require.Eventually(t, func() bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, readyURL, nil)
		if err != nil {
			return false
		}

		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		return resp.StatusCode == http.StatusOK
	}, consts.ResourceWaitTimeout, consts.PollingInterval, "VMSingle ingress route %s did not become ready", readyURL)
}

// WaitForVMSingleToBeOperational polls a VMSingle custom resource until it reports an operational status.
//
// "actual pod count: 0 less than needed" is transient: the operator may report it during
// initial PVC provisioning (WaitForFirstConsumer storage class) before the pod is created.
func WaitForVMSingleToBeOperational(ctx context.Context, t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string, vmclient vmclient.Interface, timeout time.Duration) {
	if ctx.Err() != nil {
		return
	}

	timeBoundContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(consts.PollingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-timeBoundContext.Done():
			if ctx.Err() == nil {
				require.NoError(t, fmt.Errorf("timed out waiting for VMSingle in namespace %s to become operational", namespace))
			}
			return
		case <-ticker.C:
			if pullErr := checkForImagePullErrors(timeBoundContext, t, kubeOpts); pullErr != nil {
				require.NoError(t, pullErr)
				return
			}

			list, err := vmclient.OperatorV1beta1().VMSingles(namespace).List(timeBoundContext, metav1.ListOptions{})
			if err != nil {
				continue
			}
			for i := range list.Items {
				vmSingle := &list.Items[i]
				switch vmSingle.Status.UpdateStatus {
				case vmv1beta1.UpdateStatusOperational:
					return
				case vmv1beta1.UpdateStatusFailed:
					reason := strings.TrimSpace(vmSingle.Status.Reason)
					if reason == "" {
						reason = "unknown reason"
					}
					// Transient: operator may set failed during initial PVC provisioning
					// (WaitForFirstConsumer storage class) before the pod is created.
					if strings.Contains(reason, "actual pod count: 0 less than needed") {
						helpers.Logf("VMSingle %s/%s transiently failed (PVC binding): %s — retrying", namespace, vmSingle.Name, reason)
						continue
					}
					require.NoError(t, fmt.Errorf("VMSingle %s/%s entered failed state: %s",
						namespace, vmSingle.Name, reason))
					return
				}
			}
		}
	}
}

// DeleteVMSingle deletes the specified VMSingle resource from the cluster.
// It ignores "not found" errors.
func DeleteVMSingle(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, vmsingleName string) {
	// Delete the VMSingle resource
	helpers.Logf("Deleting VMSingle %s", vmsingleName)
	k8s.RunKubectlContext(t, context.Background(), kubeOpts, "delete", "vmsingle", vmsingleName, "--ignore-not-found=true")
}
