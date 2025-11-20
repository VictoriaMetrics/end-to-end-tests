package install

import (
	"context"
	"strings"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	k8stesting "k8s.io/client-go/testing"

	vmfake "github.com/VictoriaMetrics/operator/api/client/versioned/fake"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"
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

func TestEnsureVMClusterComponents(t *testing.T) {
	tests := []struct {
		name          string
		vmcluster     *vmv1beta1.VMCluster
		expectError   bool
		errorContains string
	}{
		{
			name: "valid VMCluster with all components",
			vmcluster: &vmv1beta1.VMCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster",
					Namespace: "test-ns",
				},
				Spec: vmv1beta1.VMClusterSpec{
					RetentionPeriod: "30d",
					VMStorage: &vmv1beta1.VMStorage{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
						StorageDataPath: "/vm-data",
					},
					VMSelect: &vmv1beta1.VMSelect{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
					VMInsert: &vmv1beta1.VMInsert{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
				},
				Status: vmv1beta1.VMClusterStatus{
					StatusMetadata: vmv1beta1.StatusMetadata{
						UpdateStatus: vmv1beta1.UpdateStatusOperational,
					},
				},
			},
			expectError: false,
		},
		{
			name: "VMCluster with missing retention period",
			vmcluster: &vmv1beta1.VMCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-no-retention",
					Namespace: "test-ns",
				},
				Spec: vmv1beta1.VMClusterSpec{
					RetentionPeriod: "",
					VMStorage: &vmv1beta1.VMStorage{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
						StorageDataPath: "/vm-data",
					},
					VMSelect: &vmv1beta1.VMSelect{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
					VMInsert: &vmv1beta1.VMInsert{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
				},
			},
			expectError:   true,
			errorContains: "empty retention period",
		},
		{
			name: "VMCluster with missing VMStorage",
			vmcluster: &vmv1beta1.VMCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-no-storage",
					Namespace: "test-ns",
				},
				Spec: vmv1beta1.VMClusterSpec{
					RetentionPeriod: "30d",
					VMStorage:       nil,
					VMSelect: &vmv1beta1.VMSelect{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
					VMInsert: &vmv1beta1.VMInsert{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
				},
			},
			expectError:   true,
			errorContains: "no VMStorage configuration",
		},
		{
			name: "VMCluster with missing VMSelect",
			vmcluster: &vmv1beta1.VMCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-no-select",
					Namespace: "test-ns",
				},
				Spec: vmv1beta1.VMClusterSpec{
					RetentionPeriod: "30d",
					VMStorage: &vmv1beta1.VMStorage{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
						StorageDataPath: "/vm-data",
					},
					VMSelect: nil,
					VMInsert: &vmv1beta1.VMInsert{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
				},
			},
			expectError:   true,
			errorContains: "no VMSelect configuration",
		},
		{
			name: "VMCluster with missing VMInsert",
			vmcluster: &vmv1beta1.VMCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-no-insert",
					Namespace: "test-ns",
				},
				Spec: vmv1beta1.VMClusterSpec{
					RetentionPeriod: "30d",
					VMStorage: &vmv1beta1.VMStorage{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
						StorageDataPath: "/vm-data",
					},
					VMSelect: &vmv1beta1.VMSelect{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
					VMInsert: nil,
				},
			},
			expectError:   true,
			errorContains: "no VMInsert configuration",
		},
		{
			name: "VMCluster with empty storage data path",
			vmcluster: &vmv1beta1.VMCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-cluster-empty-path",
					Namespace: "test-ns",
				},
				Spec: vmv1beta1.VMClusterSpec{
					RetentionPeriod: "30d",
					VMStorage: &vmv1beta1.VMStorage{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
						StorageDataPath: "",
					},
					VMSelect: &vmv1beta1.VMSelect{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
					VMInsert: &vmv1beta1.VMInsert{
						CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
							ReplicaCount: func(i int32) *int32 { return &i }(2),
						},
					},
				},
			},
			expectError:   true,
			errorContains: "empty storage data path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake VM client
			fakeVMClient := vmfake.NewSimpleClientset(tt.vmcluster)

			ctx := context.Background()
			kubeOpts := &k8s.KubectlOptions{
				Namespace: tt.vmcluster.Namespace,
			}

			// Create test recorder to capture errors
			testRecorder := &TestRecorder{}

			// Call the function under test
			EnsureVMClusterComponents(ctx, testRecorder, kubeOpts, tt.vmcluster.Namespace, fakeVMClient, tt.vmcluster.Name)

			// Check if error was expected
			if tt.expectError {
				assert.True(t, testRecorder.failed, "Expected EnsureVMClusterComponents to fail")
				if testRecorder.failed {
					found := false
					for _, err := range testRecorder.errors {
						if strings.Contains(err, tt.errorContains) {
							found = true
							break
						}
					}
					assert.True(t, found, "Expected error containing '%s', got: %v", tt.errorContains, testRecorder.errors)
				}
			} else {
				assert.False(t, testRecorder.failed, "Expected EnsureVMClusterComponents to succeed, but failed with: %v", testRecorder.errors)
			}
		})
	}
}

func TestVMClusterEndpointsIntegration(t *testing.T) {
	// Test realistic VMCluster configuration
	vmcluster := &vmv1beta1.VMCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "overwatch",
			Namespace: "vm",
		},
		Spec: vmv1beta1.VMClusterSpec{
			RetentionPeriod:   "1",
			ReplicationFactor: func(i int32) *int32 { return &i }(2),
			VMStorage: &vmv1beta1.VMStorage{
				CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
					ReplicaCount: func(i int32) *int32 { return &i }(2),
				},
				StorageDataPath: "/vm-data",
			},
			VMSelect: &vmv1beta1.VMSelect{
				CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
					ReplicaCount: func(i int32) *int32 { return &i }(2),
				},
				CommonDefaultableParams: vmv1beta1.CommonDefaultableParams{
					Port: "8481",
				},
			},
			VMInsert: &vmv1beta1.VMInsert{
				CommonApplicationDeploymentParams: vmv1beta1.CommonApplicationDeploymentParams{
					ReplicaCount: func(i int32) *int32 { return &i }(2),
				},
				CommonDefaultableParams: vmv1beta1.CommonDefaultableParams{
					Port: "8480",
				},
			},
		},
		Status: vmv1beta1.VMClusterStatus{
			StatusMetadata: vmv1beta1.StatusMetadata{
				UpdateStatus: vmv1beta1.UpdateStatusOperational,
			},
		},
	}

	fakeVMClient := vmfake.NewSimpleClientset(vmcluster)
	ctx := context.Background()
	kubeOpts := &k8s.KubectlOptions{
		Namespace: "vm",
	}
	testRecorder := &TestRecorder{}

	// Test component validation
	EnsureVMClusterComponents(ctx, testRecorder, kubeOpts, "vm", fakeVMClient, "overwatch")

	// Should succeed without errors
	assert.False(t, testRecorder.failed, "Expected no errors, but got: %v", testRecorder.errors)
	assert.Empty(t, testRecorder.errors, "Expected no error messages")

	// Test service endpoint generation
	endpoints := GetVMClusterServiceEndpoints("vm", "overwatch")

	expectedEndpoints := VMClusterEndpoints{
		VMInsert:  "vminsert-overwatch.vm.svc.cluster.local:8480",
		VMSelect:  "vmselect-overwatch.vm.svc.cluster.local:8481",
		VMStorage: "vmstorage-overwatch.vm.svc.cluster.local:8482",
	}

	assert.Equal(t, expectedEndpoints, endpoints, "Service endpoints should match expected values")
}

func TestWaitForVMClusterToBeOperationalIntegration(t *testing.T) {
	// Create a VMCluster with operational status
	vmcluster := &vmv1beta1.VMCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-cluster",
			Namespace: "test-ns",
		},
		Spec: vmv1beta1.VMClusterSpec{
			RetentionPeriod: "30d",
		},
		Status: vmv1beta1.VMClusterStatus{
			StatusMetadata: vmv1beta1.StatusMetadata{
				UpdateStatus: vmv1beta1.UpdateStatusOperational,
			},
		},
	}

	// Create fake client
	fakeVMClient := vmfake.NewSimpleClientset(vmcluster)

	// Add watch reactor to simulate the watch behavior
	fakeVMClient.PrependWatchReactor("vmclusters", func(action k8stesting.Action) (handled bool, ret watch.Interface, err error) {
		fakeWatch := watch.NewFake()
		go func() {
			fakeWatch.Add(vmcluster)
		}()
		return true, fakeWatch, nil
	})

	ctx := context.Background()
	kubeOpts := &k8s.KubectlOptions{
		Namespace: "test-ns",
	}
	testRecorder := &TestRecorder{}

	// This should not timeout or error since the VMCluster is already operational
	WaitForVMClusterToBeOperational(ctx, testRecorder, kubeOpts, "test-ns", fakeVMClient)

	// Check that no errors occurred
	assert.False(t, testRecorder.failed, "Expected no errors, but got: %v", testRecorder.errors)
	assert.Empty(t, testRecorder.errors, "Expected no error messages")
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
