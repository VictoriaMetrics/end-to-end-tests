package install

import (
	"encoding/json"

	jsonpatch "github.com/evanphx/json-patch/v5"
)

// PatchOp represents a JSON patch operation
type PatchOp struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

// CreateJsonPatch converts a slice of PatchOp into a jsonpatch.Patch
func CreateJsonPatch(ops []PatchOp) (jsonpatch.Patch, error) {
	patchBytes, err := json.Marshal(ops)
	if err != nil {
		return nil, err
	}
	return jsonpatch.DecodePatch(patchBytes)
}
