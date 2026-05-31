package install

import (
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestK6BackendHealthURLUsesEndpointHost(t *testing.T) {
	got := k6BackendHealthURL("http://vmselect-vm.monitoring.svc.cluster.local:8481/select/0/prometheus/api/v1/query_range")

	require.Equal(t, "http://vmselect-vm.monitoring.svc.cluster.local:8481/health", got)
}

func TestK6BackendHealthURLUsesOverriddenEndpointHost(t *testing.T) {
	got := k6BackendHealthURL("http://vmauth-test-ns.cluster.local.nip.io/insert/0/prometheus/api/v1/import/prometheus")

	require.Equal(t, "http://vmauth-test-ns.cluster.local.nip.io/health", got)
}

func TestK6BackendHealthURLRejectsInvalidEndpoint(t *testing.T) {
	got := k6BackendHealthURL("://bad")

	require.Empty(t, got)
}

func TestK6EnvValueReturnsOverride(t *testing.T) {
	envVars := []corev1.EnvVar{
		{Name: "VMSELECT_URL", Value: "http://default/select"},
		{Name: "VMINSERT_URL", Value: "http://override/insert"},
	}

	got := k6EnvValue(envVars, "VMINSERT_URL")

	require.Equal(t, "http://override/insert", got)
}

func TestK6RunnerResourcesSetMemoryRequest(t *testing.T) {
	resources := k6RunnerResources()

	require.Equal(t, resource.MustParse("256Mi"), resources.Requests[corev1.ResourceMemory])
	require.Empty(t, resources.Limits)
}
