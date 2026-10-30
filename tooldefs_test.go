package main

import (
	"encoding/json"
	"testing"
)

// Every tool definition must be valid JSON: json.Marshal of tools/list fails silently otherwise
// and Claude desktop never gets a reply (1.18.4 shipped with a broken "system" entry).
func TestToolDefsValidJSON(t *testing.T) {
	for i, d := range toolDefs {
		var v struct {
			Name        string          `json:"name"`
			InputSchema json.RawMessage `json:"inputSchema"`
		}
		if err := json.Unmarshal(d, &v); err != nil {
			t.Errorf("toolDefs[%d] is not valid JSON: %v", i, err)
			continue
		}
		if v.Name == "" || len(v.InputSchema) == 0 {
			t.Errorf("toolDefs[%d] (%q) lacks name or inputSchema", i, v.Name)
		}
	}
	if _, err := json.Marshal(map[string]any{"tools": toolDefs}); err != nil {
		t.Fatalf("tools/list cannot be marshalled: %v", err)
	}
}
