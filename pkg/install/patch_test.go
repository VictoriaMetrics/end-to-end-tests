package install

import (
	"encoding/json"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestApplyPatches(t *testing.T) {
	originalYaml := `
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  ports:
  - port: 80
    protocol: TCP
    targetPort: 9376
`
	expectedJson := `{"apiVersion":"v1","kind":"Service","metadata":{"name":"my-service"},"spec":{"ports":[{"port":80,"protocol":"TCP","targetPort":9377}]}}`

	patchJSON := `[{"op": "replace", "path": "/spec/ports/0/targetPort", "value": 9377}]`
	patch, err := jsonpatch.DecodePatch([]byte(patchJSON))
	require.NoError(t, err)

	// Simulate logic in Install functions
	docJson, err := yaml.YAMLToJSON([]byte(originalYaml))
	require.NoError(t, err)

	patchedJson, err := patch.Apply(docJson)
	require.NoError(t, err)

	assert.JSONEq(t, expectedJson, string(patchedJson))
}

func TestApplyMultiplePatches(t *testing.T) {
	originalYaml := `
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  ports:
  - port: 80
`
	patch1JSON := `[{"op": "add", "path": "/spec/selector", "value": {"app": "MyApp"}}]`
	patch2JSON := `[{"op": "replace", "path": "/metadata/name", "value": "new-service"}]`

	patch1, err := jsonpatch.DecodePatch([]byte(patch1JSON))
	require.NoError(t, err)
	patch2, err := jsonpatch.DecodePatch([]byte(patch2JSON))
	require.NoError(t, err)

	patches := []jsonpatch.Patch{patch1, patch2}

	docJson, err := yaml.YAMLToJSON([]byte(originalYaml))
	require.NoError(t, err)

	for _, p := range patches {
		docJson, err = p.Apply(docJson)
		require.NoError(t, err)
	}

	var result map[string]interface{}
	err = json.Unmarshal(docJson, &result)
	require.NoError(t, err)

	metadata := result["metadata"].(map[string]interface{})
	assert.Equal(t, "new-service", metadata["name"])

	spec := result["spec"].(map[string]interface{})
	selector := spec["selector"].(map[string]interface{})
	assert.Equal(t, "MyApp", selector["app"])
}
