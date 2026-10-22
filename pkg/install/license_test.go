package install

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

func TestVLSingleLicenseFiles(t *testing.T) {
	origLicense, origVersion := consts.LicenseFile(), consts.VLVersion()
	defer consts.SetLicenseFile(origLicense)
	defer consts.SetVLVersion(origVersion)

	tests := []struct {
		name, license, version string
		want                   map[string]string
	}{
		{"no license", "", "v1.52.1-enterprise", nil},
		{"community image", "/tmp/lic", "v1.53.0", nil},
		{"enterprise image", "/tmp/lic", "v1.52.1-enterprise", map[string]string{"license.key": "/tmp/lic"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			consts.SetLicenseFile(tc.license)
			consts.SetVLVersion(tc.version)
			require.Equal(t, tc.want, VLSingleLicenseFiles())
		})
	}
}

func TestVLClusterReadinessClusterVersion(t *testing.T) {
	got := vlclusterIngressReadinessFromSpec(t, []byte(`{"metadata":{"name":"vl"},"spec":{"clusterVersion":"v1.52.1-enterprise"}}`))
	require.Equal(t, "v1.52.1-enterprise", got.ClusterVersion)
	require.True(t, isEnterpriseTag(got.ClusterVersion))
	require.False(t, isEnterpriseTag("v1.53.0"))
}
