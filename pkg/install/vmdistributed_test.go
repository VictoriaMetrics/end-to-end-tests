package install

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

func TestVMDistributedRemoteWriteURLUsesTenantInsertPath(t *testing.T) {
	consts.SetNginxHost("cluster.local")

	got := VMDistributedRemoteWriteURL("test-ns")

	require.Equal(t, "http://vmauth-test-ns.cluster.local.nip.io/insert/0/prometheus/api/v1/write", got)
}

func TestVMDistributedImportURLUsesTenantImportPath(t *testing.T) {
	consts.SetNginxHost("cluster.local")

	got := VMDistributedImportURL("test-ns")

	require.Equal(t, "http://vmauth-test-ns.cluster.local.nip.io/insert/0/prometheus/api/v1/import/prometheus", got)
}

func TestBuildVMDistributedManifestParsesCleanly(t *testing.T) {
	consts.SetDistributedZones("europe-central2-a,europe-central2-b,europe-central2-c")

	manifest := buildVMDistributedManifest("vmks", "test-ns", "vmauth-test-ns.example.com")

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(manifest), &parsed))
	require.Contains(t, manifest, "class_name: nginx")
	require.Contains(t, manifest, "host: vmauth-test-ns.example.com")
	require.Contains(t, manifest, "- name: europe-central2-a")
	require.Contains(t, manifest, "unauthorizedUserAccessSpec:")
	require.Contains(t, manifest, "- name: write")
	require.Contains(t, manifest, "- name: read")
}

func TestBuildVMDistributedZoneDisruptionChaos(t *testing.T) {
	manifest := buildVMDistributedZoneDisruptionChaos("test-ns", "europe-central2-a", 3*time.Minute)

	for _, want := range []string{
		"kind: NetworkChaos",
		"name: zone-disruption-europe-central2-a",
		"duration: 3m",
		"- test-ns",
		"app.kubernetes.io/instance: europe-central2-a",
		"action: loss",
		"direction: to",
		"loss: '100'",
	} {
		require.True(t, strings.Contains(manifest, want), "manifest should contain %q", want)
	}
}
