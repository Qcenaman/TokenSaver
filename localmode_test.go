package main

import (
	"encoding/json"
	"testing"
)

// withLocalMode runs a test as the local-model server (what ContextSaver was).
func withLocalMode(t *testing.T) {
	t.Helper()
	old := localMode
	localMode = true
	t.Cleanup(func() { localMode = old })
}

func TestDetectLocalMode(t *testing.T) {
	t.Setenv("TOKENSAVER_MODE", "")
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{`D:\\Claude Apps\\TokenSaver\\tokensaver.exe`, "mcp"}, false},
		{[]string{`D:\\Claude Apps\\ContextSaver\\contextsaver.exe`, "mcp"}, true},
		{[]string{"/usr/local/bin/contextsaver", "mcp"}, true},
		{[]string{"tokensaver", "mcp", "--local"}, true},
		{[]string{"tokensaver", "mcp", "--for", "cline"}, true},
	} {
		if got := detectLocalMode(c.args); got != c.want {
			t.Errorf("%v: got %v, want %v", c.args, got, c.want)
		}
	}
}

func TestLocalModeListsOneTool(t *testing.T) {
	withLocalMode(t)
	r := handleRPC(rpcReq{ID: []byte("1"), Method: "tools/list"})
	res := r["result"].(map[string]any)
	if n := len(res["tools"].([]json.RawMessage)); n != 1 {
		t.Fatalf("local mode lists %d tools, want 1 (pc)", n)
	}
	localMode = false
	r = handleRPC(rpcReq{ID: []byte("1"), Method: "tools/list"})
	if r["result"] == nil {
		t.Fatal("no tools in Claude mode")
	}
}
