package install

import (
	"encoding/json"
	"testing"
)

func TestBaseVLAgentJSON(t *testing.T) {
	data, err := baseVLAgentJSON()
	if err != nil {
		t.Fatalf("baseVLAgentJSON() error = %v", err)
	}

	var got struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got.APIVersion != "operator.victoriametrics.com/v1" || got.Kind != "VLAgent" {
		t.Fatalf("unexpected type: %#v", got)
	}
}
