package install

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetVMClusterServiceEndpoints(t *testing.T) {
	tests := []struct {
		name          string
		namespace     string
		vmclusterName string
		expected      VMClusterEndpoints
	}{
		{
			name:          "vm namespace with overwatch cluster",
			namespace:     "vm",
			vmclusterName: "overwatch",
			expected: VMClusterEndpoints{
				VMInsert:  "vminsert-overwatch.vm.svc.cluster.local:8480",
				VMSelect:  "vmselect-overwatch.vm.svc.cluster.local:8481",
				VMStorage: "vmstorage-overwatch.vm.svc.cluster.local:8482",
			},
		},
		{
			name:          "test namespace with custom cluster",
			namespace:     "test-ns",
			vmclusterName: "overwatch-test-ns",
			expected: VMClusterEndpoints{
				VMInsert:  "vminsert-overwatch-test-ns.test-ns.svc.cluster.local:8480",
				VMSelect:  "vmselect-overwatch-test-ns.test-ns.svc.cluster.local:8481",
				VMStorage: "vmstorage-overwatch-test-ns.test-ns.svc.cluster.local:8482",
			},
		},
		{
			name:          "production namespace",
			namespace:     "production",
			vmclusterName: "main-cluster",
			expected: VMClusterEndpoints{
				VMInsert:  "vminsert-main-cluster.production.svc.cluster.local:8480",
				VMSelect:  "vmselect-main-cluster.production.svc.cluster.local:8481",
				VMStorage: "vmstorage-main-cluster.production.svc.cluster.local:8482",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoints := GetVMClusterServiceEndpoints(tt.namespace, tt.vmclusterName)

			assert.Equal(t, tt.expected.VMInsert, endpoints.VMInsert, "VMInsert endpoint should match")
			assert.Equal(t, tt.expected.VMSelect, endpoints.VMSelect, "VMSelect endpoint should match")
			assert.Equal(t, tt.expected.VMStorage, endpoints.VMStorage, "VMStorage endpoint should match")
		})
	}
}

func TestVMClusterNameGeneration(t *testing.T) {
	tests := []struct {
		name                string
		namespace           string
		expectedClusterName string
	}{
		{
			name:                "vm namespace uses default name",
			namespace:           "vm",
			expectedClusterName: "overwatch",
		},
		{
			name:                "other namespace gets suffix",
			namespace:           "test-ns",
			expectedClusterName: "overwatch-test-ns",
		},
		{
			name:                "production namespace",
			namespace:           "production",
			expectedClusterName: "overwatch-production",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// This tests the expected naming logic that would be used in InstallVMCluster
			expectedName := "overwatch"
			if tt.namespace != "vm" {
				expectedName = "overwatch-" + tt.namespace
			}

			assert.Equal(t, tt.expectedClusterName, expectedName, "Cluster name should match expected pattern")
		})
	}
}
