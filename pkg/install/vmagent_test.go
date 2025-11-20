package install

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/gruntwork-io/terratest/modules/k8s"
	"github.com/stretchr/testify/assert"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	k8stesting "k8s.io/client-go/testing"

	vmfake "github.com/VictoriaMetrics/operator/api/client/versioned/fake"
	vmv1beta1 "github.com/VictoriaMetrics/operator/api/operator/v1beta1"

	"github.com/VictoriaMetrics/end-to-end-tests/pkg/consts"
)

// TestRecorder implements terratesting.TestingT for simple error recording
type TestRecorder struct {
	errors []string
	failed bool
}

func (r *TestRecorder) Fail() {
	r.failed = true
}

func (r *TestRecorder) FailNow() {
	r.failed = true
	r.errors = append(r.errors, "FailNow called")
}

func (r *TestRecorder) Fatal(args ...interface{}) {
	r.failed = true
	r.errors = append(r.errors, fmt.Sprint(args...))
}

func (r *TestRecorder) Fatalf(format string, args ...interface{}) {
	r.failed = true
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *TestRecorder) Error(args ...interface{}) {
	r.failed = true
	r.errors = append(r.errors, fmt.Sprint(args...))
}

func (r *TestRecorder) Errorf(format string, args ...interface{}) {
	r.failed = true
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

func (r *TestRecorder) Name() string {
	return "TestRecorder"
}

func TestWaitForVMAgentToBeOperational(t *testing.T) {
	// Create a VMAgent with operational status
	vmAgent := &vmv1beta1.VMAgent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-agent",
			Namespace: "test-ns",
		},
		Status: vmv1beta1.VMAgentStatus{
			StatusMetadata: vmv1beta1.StatusMetadata{
				UpdateStatus: vmv1beta1.UpdateStatusOperational,
			},
		},
	}

	// Create fake client
	fakeVMClient := vmfake.NewSimpleClientset(vmAgent)

	// Add watch reactor to simulate the watch behavior
	fakeVMClient.PrependWatchReactor("vmagents", func(action k8stesting.Action) (handled bool, ret watch.Interface, err error) {
		fakeWatch := watch.NewFake()
		go func() {
			fakeWatch.Add(vmAgent)
		}()
		return true, fakeWatch, nil
	})

	ctx := context.Background()
	kubeOpts := &k8s.KubectlOptions{
		Namespace: "test-ns",
	}
	testRecorder := &TestRecorder{}

	// This should not timeout or error since the VMAgent is already operational
	WaitForVMAgentToBeOperational(ctx, testRecorder, kubeOpts, "test-ns", fakeVMClient)

	// Check that no errors occurred
	assert.Empty(t, testRecorder.errors, "Expected no errors")
	assert.False(t, testRecorder.failed, "Expected failed to be false")
}

func TestVMAgentURLReplacement(t *testing.T) {
	// Test vmagent.yaml content similar to what's in the actual manifest file
	originalVMAgentContent := `apiVersion: operator.victoriametrics.com/v1beta1
kind: VMAgent
metadata:
  finalizers:
  - apps.victoriametrics.com/finalizer
  name: vmks
spec:
  remoteWrite:
  - url: http://vmsingle-overwatch.vm.svc.cluster.local.:8428/prometheus/api/v1/write`

	testCases := []struct {
		name      string
		namespace string
	}{
		{"vm namespace", "vm"},
		{"test namespace", "test-ns"},
		{"production namespace", "production"},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			// Get service names for the namespace
			vmInsertSvc := consts.GetVMInsertSvc("vmks", tt.namespace)
			vmSingleSvc := consts.GetVMSingleSvc("overwatch", tt.namespace)

			// Define old and new URLs
			oldVMInsertURL := "http://vminsert-vmks.vm.svc.cluster.local.:8480/insert/0/prometheus/api/v1/write"
			newVMInsertURL := "http://" + vmInsertSvc + "/insert/0/prometheus/api/v1/write"

			oldVMSingleURL := "http://vmsingle-overwatch.vm.svc.cluster.local.:8428/prometheus/api/v1/write"
			newVMSingleURL := "http://" + vmSingleSvc + "/prometheus/api/v1/write"

			// Perform replacements
			updatedContent := strings.ReplaceAll(originalVMAgentContent, oldVMInsertURL, newVMInsertURL)
			updatedContent = strings.ReplaceAll(updatedContent, oldVMSingleURL, newVMSingleURL)

			// Verify the replacement occurred (except for vm namespace where URLs might be the same)
			if tt.namespace != "vm" {
				assert.NotEqual(t, originalVMAgentContent, updatedContent, "URL replacement should occur")
			}

			// Verify that the content still contains the expected structure
			assert.Contains(t, updatedContent, "kind: VMAgent", "YAML structure should be maintained")
			assert.Contains(t, updatedContent, "remoteWrite:", "remoteWrite configuration should be maintained")
		})
	}
}

func TestVMAgentURLReplacementFormat(t *testing.T) {
	// Test that service functions return the expected format for URL construction
	namespace := "test-ns"

	vmInsertSvc := consts.GetVMInsertSvc("vmks", namespace)
	expectedVMInsertFormat := "vminsert-vmks.test-ns.svc.cluster.local:8480"
	assert.Equal(t, expectedVMInsertFormat, vmInsertSvc, "GetVMInsertSvc should return expected format")

	vmSingleSvc := consts.GetVMSingleSvc("overwatch", namespace)
	expectedVMSingleFormat := "vmsingle-overwatch.test-ns.svc.cluster.local:8428"
	assert.Equal(t, expectedVMSingleFormat, vmSingleSvc, "GetVMSingleSvc should return expected format")

	// Test full URL construction
	vmInsertFullURL := "http://" + vmInsertSvc + "/insert/0/prometheus/api/v1/write"
	expectedVMInsertFullURL := "http://vminsert-vmks.test-ns.svc.cluster.local:8480/insert/0/prometheus/api/v1/write"
	assert.Equal(t, expectedVMInsertFullURL, vmInsertFullURL, "VMInsert full URL should be correct")

	vmSingleFullURL := "http://" + vmSingleSvc + "/prometheus/api/v1/write"
	expectedVMSingleFullURL := "http://vmsingle-overwatch.test-ns.svc.cluster.local:8428/prometheus/api/v1/write"
	assert.Equal(t, expectedVMSingleFullURL, vmSingleFullURL, "VMSingle full URL should be correct")
}

func TestVMAgentURLReplacementEdgeCases(t *testing.T) {
	// Test with content that doesn't have the expected URL patterns
	noURLContent := `apiVersion: operator.victoriametrics.com/v1beta1
kind: VMAgent
metadata:
  name: test-agent
spec:
  remoteWrite:
  - url: http://some-other-service.example.com/write`

	// Should not modify content that doesn't have the expected patterns
	vmInsertSvc := consts.GetVMInsertSvc("vmks", "test")
	vmSingleSvc := consts.GetVMSingleSvc("overwatch", "test")

	oldVMInsertURL := "http://vminsert-vmks.vm.svc.cluster.local:8480/insert/0/prometheus/api/v1/write"
	newVMInsertURL := "http://" + vmInsertSvc + "/insert/0/prometheus/api/v1/write"

	oldVMSingleURL := "http://vmsingle-overwatch.vm.svc.cluster.local:8428/prometheus/api/v1/write"
	newVMSingleURL := "http://" + vmSingleSvc + "/prometheus/api/v1/write"

	updatedContent := strings.ReplaceAll(noURLContent, oldVMInsertURL, newVMInsertURL)
	updatedContent = strings.ReplaceAll(updatedContent, oldVMSingleURL, newVMSingleURL)

	assert.Equal(t, noURLContent, updatedContent, "Content without expected URL patterns should not be modified")
}

func TestVMAgentCompleteReplacement(t *testing.T) {
	// Test complete vmagent.yaml content replacement with both URLs
	vmagentContent := `apiVersion: operator.victoriametrics.com/v1beta1
kind: VMAgent
metadata:
  finalizers:
  - apps.victoriametrics.com/finalizer
  name: vmks
spec:
  remoteWrite:
  - url: http://vmsingle-overwatch.vm.svc.cluster.local:8428/prometheus/api/v1/write`

	namespace := "production"
	vmSingleSvc := consts.GetVMSingleSvc("overwatch", namespace)

	// Use the same replacement logic as in the actual function
	oldVMSingleURL := "http://vmsingle-overwatch.vm.svc.cluster.local:8428/prometheus/api/v1/write"
	newVMSingleURL := "http://" + vmSingleSvc + "/prometheus/api/v1/write"

	updatedContent := strings.ReplaceAll(vmagentContent, oldVMSingleURL, newVMSingleURL)

	expectedVMSingleURL := "http://vmsingle-overwatch.production.svc.cluster.local:8428/prometheus/api/v1/write"
	assert.Contains(t, updatedContent, expectedVMSingleURL, "Updated content should contain the new VMSingle URL")
	assert.NotContains(t, updatedContent, oldVMSingleURL, "Old VMSingle URL should be replaced")

	// Verify the YAML structure is maintained
	assert.Contains(t, updatedContent, "kind: VMAgent", "YAML structure should be maintained")
}
