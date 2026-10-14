package install

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPrepareChaosManifestReplacesVLNamespaceAndPodNames(t *testing.T) {
	manifest := `
selector:
  namespaces:
    - vl
  labelSelectors:
    statefulset.kubernetes.io/pod-name: vlstorage-vl-0
`

	assert.Equal(t, `
selector:
  namespaces:
    - vl-chaos-test
  labelSelectors:
    statefulset.kubernetes.io/pod-name: vlstorage-vl-chaos-test-0
`, prepareChaosManifest(manifest, "vl-chaos-test"))
}
