package install

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestPrepareChaosManifestUsesClusterName(t *testing.T) {
	vlManifest := `
selector:
  namespaces:
    - vl
  labelSelectors:
    statefulset.kubernetes.io/pod-name: vlstorage-vl-0
`

	assert.Equal(t, `
selector:
  namespaces:
    - vl-chaos-test-abcdef
  labelSelectors:
    statefulset.kubernetes.io/pod-name: vlstorage-vl-chaos-test-0
`, prepareChaosManifest(vlManifest, "vl-chaos-test-abcdef", "vl-chaos-test"))

	vmManifest := `
selector:
  namespaces:
    - vm
  labelSelectors:
    statefulset.kubernetes.io/pod-name: vmstorage-vm-1
`
	assert.Equal(t, `
selector:
  namespaces:
    - vm-chaos-test-abcdef
  labelSelectors:
    statefulset.kubernetes.io/pod-name: vmstorage-vm-chaos-test-1
`, prepareChaosManifest(vmManifest, "vm-chaos-test-abcdef", "vm-chaos-test"))
}

func TestWorkflowCompletion(t *testing.T) {
	workflow := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []interface{}{map[string]interface{}{"type": "Accomplished", "status": "True"}},
		},
	}}
	assert.True(t, hasChaosCondition(workflow, "Accomplished"))
	assert.False(t, hasChaosCondition(workflow, "AllRecovered"))
}

func TestPodRestarted(t *testing.T) {
	before := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{UID: types.UID("old")},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "storage", RestartCount: 0},
		}},
	}
	same := before.DeepCopy()
	assert.False(t, PodRestarted(before, same))

	recreated := before.DeepCopy()
	recreated.UID = types.UID("new")
	assert.True(t, PodRestarted(before, recreated))

	restarted := before.DeepCopy()
	restarted.Status.ContainerStatuses[0].RestartCount++
	assert.True(t, PodRestarted(before, restarted))
}
