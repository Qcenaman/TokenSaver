package main

import (
	"encoding/json"
	"os"
	"path/filepath"
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
		{[]string{"tokensaver", "--local", "start", "coder"}, true},
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

func TestCommandArgsDropsLeadingLocal(t *testing.T) {
	got := commandArgs([]string{"--local", "start", "coder"})
	if len(got) != 2 || got[0] != "start" {
		t.Fatalf("got %v", got)
	}
	if got := commandArgs([]string{"mcp", "--local"}); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func TestLocalRootPrefersCurrentThenLegacy(t *testing.T) {
	dir := t.TempDir()
	current, legacy := filepath.Join(dir, "local"), filepath.Join(dir, "old")
	if got := localRoot(current, legacy); got != current {
		t.Fatalf("neither exists: got %s", got)
	}
	os.Mkdir(legacy, 0o755)
	if got := localRoot(current, legacy); got != legacy {
		t.Fatalf("only legacy: got %s", got)
	}
	os.Mkdir(current, 0o755)
	if got := localRoot(current, legacy); got != current {
		t.Fatalf("both: got %s", got)
	}
}
