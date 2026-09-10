package install

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
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

func TestK6RunnerResources(t *testing.T) {
	resources := k6RunnerResources()

	require.Equal(t, resource.MustParse("500m"), resources.Requests[corev1.ResourceCPU])
	require.Equal(t, resource.MustParse("512Mi"), resources.Requests[corev1.ResourceMemory])
	require.Equal(t, resource.MustParse("1500m"), resources.Limits[corev1.ResourceCPU])
	require.Equal(t, resource.MustParse("768Mi"), resources.Limits[corev1.ResourceMemory])
}

func TestK6RunnerPodUsesPinnedImage(t *testing.T) {
	runner := k6RunnerPod(nil)

	require.Equal(t, k6RunnerImage, runner.Image)
	require.NotEmpty(t, runner.Image)
	require.False(t, strings.HasSuffix(runner.Image, ":latest"))
}

func TestWaitForK6TestRunChecksStageAfterWatch(t *testing.T) {
	const namespace = "test"
	const name = "scenario"
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "k6.io/v1alpha1",
		"kind":       "TestRun",
		"metadata":   map[string]interface{}{"name": name, "namespace": namespace},
		"status":     map[string]interface{}{"stage": "started"},
	}}
	client := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{testRunGVR: "TestRunList"}, obj)
	client.PrependWatchReactor("testruns", func(action k8stesting.Action) (bool, watch.Interface, error) {
		finished := obj.DeepCopy()
		_ = unstructured.SetNestedField(finished.Object, "finished", "status", "stage")
		require.NoError(t, client.Tracker().Update(testRunGVR, finished, namespace))
		return true, watch.NewRaceFreeFake(), nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	waitForK6TestRun(ctx, t, client.Resource(testRunGVR).Namespace(namespace), namespace, name)
}
