package main

import (
	"encoding/json"
	"strings"
)

// MCP tool annotations, so a chat app can tell the tools apart and need not ask before each call.
// Only what is true for every action of a tool is said:
// readOnlyTools never change anything on the PC;
// nonDestructiveTools write (files, commits, caches) but never delete or overwrite what the user
// has. A tool with even one deleting action (run, run_job, saved, system, window, browser,
// office, rag, job, phone, peer) stays unlabelled.
var (
	readOnlyTools       = map[string]bool{"scan": true, "find": true, "read": true, "outline": true, "web": true}
	nonDestructiveTools = map[string]bool{"check": true, "git": true, "media": true, "chats": true, "local_ai": true}
)

func annotateDef(d json.RawMessage) json.RawMessage {
	var n struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(d, &n) != nil {
		return d
	}
	var note string
	switch {
	case readOnlyTools[n.Name]:
		note = `"annotations":{"readOnlyHint":true},`
	case nonDestructiveTools[n.Name]:
		note = `"annotations":{"readOnlyHint":false,"destructiveHint":false},`
	default:
		return d
	}
	pre := `{"name":"` + n.Name + `",`
	if !strings.HasPrefix(string(d), pre) {
		return d
	}
	return json.RawMessage(pre + note + string(d)[len(pre):])
}

func annotateDefs(defs []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, len(defs))
	for i, d := range defs {
		out[i] = annotateDef(d)
	}
	return out
}
