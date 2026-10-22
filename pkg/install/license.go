package install

import (
	"context"
	"fmt"
	"strings"

	jsonpatch "github.com/evanphx/json-patch/v5"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/gruntwork-io/terratest/modules/k8s"
	terratesting "github.com/gruntwork-io/terratest/modules/testing"
	"github.com/stretchr/testify/require"
)

// licensePatch builds a JSON patch that sets /spec/license on a VictoriaMetrics
// enterprise CR (VMAgent/VMCluster/VMSingle) to reference the license secret.
func licensePatch() (jsonpatch.Patch, error) {
	patchJSON := fmt.Sprintf(`[{
		"op": "add",
		"path": "/spec/license",
		"value": {"keyRef": {"name": %q, "key": %q}}
	}]`, consts.LicenseSecretName, consts.LicenseSecretKey)
	return jsonpatch.DecodePatch([]byte(patchJSON))
}

// isEnterpriseTag reports whether an image tag is an enterprise build. The license is only
// passed to those: community binaries reject the license flags.
func isEnterpriseTag(tag string) bool {
	return strings.Contains(tag, "-enterprise")
}

// VLSingleLicenseFiles returns helm --set-file values that license victoria-logs-single.
// It is empty unless a license file is set and the configured VL image is enterprise.
func VLSingleLicenseFiles() map[string]string {
	if consts.LicenseFile() == "" || !isEnterpriseTag(consts.VLVersion()) {
		return nil
	}
	return map[string]string{"license.key": consts.LicenseFile()}
}

// appendLicensePatch appends the license patch to jsonPatches if a license file was
// configured; otherwise it returns jsonPatches unchanged.
func appendLicensePatch(t terratesting.TestingT, jsonPatches []jsonpatch.Patch) []jsonpatch.Patch {
	if consts.LicenseFile() == "" {
		return jsonPatches
	}

	patch, err := licensePatch()
	require.NoError(t, err)
	return append(jsonPatches, patch)
}

// ensureLicenseSecret applies the license secret into namespace if a license file was
// configured; otherwise it is a no-op.
func ensureLicenseSecret(t terratesting.TestingT, kubeOpts *k8s.KubectlOptions, namespace string) {
	if consts.LicenseFile() == "" {
		return
	}

	secretYaml, err := consts.PrepareLicenseSecret(namespace)
	require.NoError(t, err)

	// Avoid KubectlApplyFromString wrapper here; it logs manifest contents.
	k8s.KubectlApplyFromStringContext(t, context.Background(), kubeOpts, secretYaml)
}
