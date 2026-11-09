package main

import (
	"encoding/json"
	"strings"
)

// readOnlyTools never change anything on the PC in any of their actions. They carry the MCP
// readOnlyHint so chat clients can tell them from the tools that write, and need not ask
// before each call. A tool with even one writing action stays unlabelled.
var readOnlyTools = map[string]bool{"scan": true, "find": true, "read": true, "outline": true, "web": true}

func annotateDef(d json.RawMessage) json.RawMessage {
	var n struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(d, &n) != nil || !readOnlyTools[n.Name] {
		return d
	}
	pre := `{"name":"` + n.Name + `",`
	if !strings.HasPrefix(string(d), pre) {
		return d
	}
	return json.RawMessage(pre + `"annotations":{"readOnlyHint":true},` + string(d)[len(pre):])
}

func annotateDefs(defs []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(defs))
	for i, d := range defs {
		out[i] = annotateDef(d)
	}
	return out
}
