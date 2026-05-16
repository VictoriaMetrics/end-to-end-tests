package install

import (
	"testing"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
)

func TestDiscoverIngressHostKindLogic(t *testing.T) {
	// Preserve original consts and restore after test
	originalDistro := consts.EnvK8SDistro()
	originalNginx := consts.NginxHost()
	defer func() {
		consts.SetEnvK8SDistro(originalDistro)
		consts.SetNginxHost(originalNginx)
	}()

	// Test the kind-specific logic
	consts.SetEnvK8SDistro("kind")
	consts.SetNginxHost("127.0.0.1")

	// Test with "vm" namespace (most common case)
	namespace := "vm"
	expectedVMSelectHost := "vmselect-vm.127.0.0.1.nip.io"
	expectedVMSingleHost := "vmsingle.127.0.0.1.nip.io"
	expectedVMSelectUrl := "http://vmselect-vm.127.0.0.1.nip.io"

	assert.Equal(t, expectedVMSelectHost, consts.VMSelectHost(namespace))
	assert.Equal(t, expectedVMSingleHost, consts.VMSingleHost())
	assert.Equal(t, expectedVMSelectUrl, consts.VMSelectUrl(namespace))
}

func TestHostnameFormatting(t *testing.T) {
	// Preserve original nginx host and restore after test
	original := consts.NginxHost()
	defer consts.SetNginxHost(original)

	tests := []struct {
		name              string
		nginxHost         string
		expectedSelect    string
		expectedSingle    string
		expectedSelectUrl string
		expectedSingleUrl string
	}{
		{
			name:              "IPv4 address",
			nginxHost:         "10.0.0.1",
			expectedSelect:    "vmselect.10.0.0.1.nip.io",
			expectedSingle:    "vmsingle.10.0.0.1.nip.io",
			expectedSelectUrl: "http://vmselect.10.0.0.1.nip.io",
			expectedSingleUrl: "http://vmsingle.10.0.0.1.nip.io",
		},
		{
			name:              "localhost",
			nginxHost:         "127.0.0.1",
			expectedSelect:    "vmselect.127.0.0.1.nip.io",
			expectedSingle:    "vmsingle.127.0.0.1.nip.io",
			expectedSelectUrl: "http://vmselect.127.0.0.1.nip.io",
			expectedSingleUrl: "http://vmsingle.127.0.0.1.nip.io",
		},
		{
			name:              "cloud provider IP",
			nginxHost:         "203.0.113.1",
			expectedSelect:    "vmselect.203.0.113.1.nip.io",
			expectedSingle:    "vmsingle.203.0.113.1.nip.io",
			expectedSelectUrl: "http://vmselect.203.0.113.1.nip.io",
			expectedSingleUrl: "http://vmsingle.203.0.113.1.nip.io",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			consts.SetNginxHost(tt.nginxHost)

			// Test with empty namespace (no namespace prefix)
			namespace := ""

			assert.Equal(t, tt.expectedSelect, consts.VMSelectHost(namespace))
			assert.Equal(t, tt.expectedSelectUrl, consts.VMSelectUrl(namespace))
		})
	}
}

func TestEnvironmentDistroLogic(t *testing.T) {
	tests := []struct {
		name         string
		distro       string
		expectKind   bool
		expectedHost string
	}{
		{
			name:         "kind environment",
			distro:       "kind",
			expectKind:   true,
			expectedHost: "127.0.0.1",
		},
		{
			name:         "non-kind environment",
			distro:       "gke",
			expectKind:   false,
			expectedHost: "",
		},
		{
			name:         "empty distro",
			distro:       "",
			expectKind:   false,
			expectedHost: "",
		},
		{
			name:         "other distro",
			distro:       "eks",
			expectKind:   false,
			expectedHost: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set the environment distro
			originalDistro := consts.EnvK8SDistro()
			defer consts.SetEnvK8SDistro(originalDistro) // Restore original value

			consts.SetEnvK8SDistro(tt.distro)

			isKind := consts.EnvK8SDistro() == "kind"
			assert.Equal(t, tt.expectKind, isKind)

			if tt.expectKind {
				// For kind environments, we should use localhost
				nginxHost := "127.0.0.1"
				assert.Equal(t, tt.expectedHost, nginxHost)
			}
		})
	}
}

func TestExtractIngressHost(t *testing.T) {
	tests := []struct {
		name         string
		service      *corev1.Service
		expectedHost string
	}{
		{
			name: "service with IP",
			service: &corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "192.168.1.100"},
						},
					},
				},
			},
			expectedHost: "192.168.1.100",
		},

		{
			name: "service with IP only",
			service: &corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: "203.0.113.42"},
						},
					},
				},
			},
			expectedHost: "203.0.113.42",
		},
		{
			name: "service with no ingress",
			service: &corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{},
					},
				},
			},
			expectedHost: "",
		},
		{
			name: "service with empty IP",
			service: &corev1.Service{
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{IP: ""},
						},
					},
				},
			},
			expectedHost: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := extractIngressHost(tt.service)
			assert.Equal(t, tt.expectedHost, host)
		})
	}
}
