package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fullTestHome(t *testing.T) {
	t.Helper()
	old := appHome
	appHome = t.TempDir()
	t.Cleanup(func() { appHome = old })
}

func TestFullOutputClientDetection(t *testing.T) {
	fullTestHome(t)
	for name, want := range map[string]bool{"Perplexity": true, "perplexity-desktop": true, "claude-ai": false, "Claude Desktop": false, "RemoteAI": false, "": false} {
		if got := isFullOutputClient(name); got != want {
			t.Errorf("%q: %v, want %v", name, got, want)
		}
	}
	if !isFullOutputClient("", "Mozilla/5.0 Perplexity/1.2") {
		t.Error("the User-Agent must count when there is no client name")
	}
	// the list comes from config.json, re-read on every check
	os.WriteFile(filepath.Join(appHome, "config.json"), []byte(`{"full_output_clients":["cursor"],"full_output_max_chars":1234}`), 0o644)
	if isFullOutputClient("Perplexity") || !isFullOutputClient("Cursor") || fullOutputMaxChars() != 1234 {
		t.Error("full_output_clients / full_output_max_chars from config.json were not used")
	}
}

func TestFullOutputCaps(t *testing.T) {
	fullTestHome(t)
	if outCap(false, 0, 6000, 30000) != 6000 || outCap(false, 99999, 6000, 30000) != 30000 {
		t.Error("Claude's caps changed")
	}
	if outCap(true, 0, 6000, 30000) != fullOutputDefaultMax {
		t.Error("a full-output call must default to full_output_max_chars")
	}
	if outCap(true, 500, 6000, 30000) != 500 || countCap(true, 5, 40, 1<<30) != 5 {
		t.Error("an argument the client sent must still win")
	}
	if outCap(true, 10<<20, 6000, 30000) != fullOutputDefaultMax {
		t.Error("never above full_output_max_chars")
	}
}

// a 300-line file: Claude gets the head, Perplexity the whole file, and lines still wins
func TestFullOutputRead(t *testing.T) {
	fullTestHome(t)
	p := filepath.Join(t.TempDir(), "big.txt")
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	os.WriteFile(p, []byte(b.String()), 0o644)
	read := func(args map[string]any, full bool) string {
		raw, _ := json.Marshal(args)
		if full {
			raw = markFullOutput(raw)
		}
		s, _, err := toolRead(raw)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	claude := read(map[string]any{"path": p}, false)
	if strings.Contains(claude, "line 300") || !strings.Contains(claude, "more lines") {
		t.Errorf("Claude's read must stay short:\n%s", claude[len(claude)-60:])
	}
	px := read(map[string]any{"path": p}, true)
	if !strings.Contains(px, "300: line 300") || strings.Contains(px, "more lines") {
		t.Error("Perplexity's read must return the whole file")
	}
	if s := read(map[string]any{"path": p, "lines": 5}, true); strings.Contains(s, "6: line 6") {
		t.Error("an explicit lines argument must still win for Perplexity")
	}
}

// a command with a long output: Claude gets error lines and a tail, Perplexity all of it
func TestFullOutputRun(t *testing.T) {
	fullTestHome(t)
	id := "20261114-000000000-test"
	os.MkdirAll(filepath.Dir(outputPath(id)), 0o755)
	var b strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&b, "out %d\n", i)
	}
	os.WriteFile(outputPath(id), []byte(b.String()), 0o644)
	code := 0
	st := &JobState{ID: id, Kind: "run", Status: "done", ExitCode: &code, Started: time.Now(), Ended: time.Now()}
	if s := formatState(st); strings.Contains(s, "out 10\n") || !strings.Contains(s, "out 200") {
		t.Errorf("Claude's run must stay a tail:\n%s", s)
	}
	if s := formatStateOpt(st, true); !strings.Contains(s, "out 1\n") || !strings.Contains(s, "out 200") {
		t.Errorf("Perplexity's run must carry the whole output:\n%.300s", s)
	}
	tail := 3
	st.Tail = &tail
	if s := formatStateOpt(st, true); strings.Contains(s, "out 100") {
		t.Error("an explicit tail must still win for Perplexity")
	}
}

// over HTTP: initialize hands out a session; the session decides tools/list and tools/call
func TestFullOutputHTTPSession(t *testing.T) {
	fullTestHome(t)
	p := filepath.Join(t.TempDir(), "big.txt")
	os.WriteFile(p, []byte(strings.Repeat("x\n", 100)+"THE END\n"), 0o644)
	allow := func(*http.Request) bool { return true }
	post := func(body, session, agent string) (*httptest.ResponseRecorder, map[string]any) {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/c/x/mcp", strings.NewReader(body))
		if session != "" {
			r.Header.Set("Mcp-Session-Id", session)
		}
		r.Header.Set("User-Agent", agent)
		w := httptest.NewRecorder()
		serveMCPHTTP(w, r, allow, "connector")
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}
	toolsText := func(out map[string]any) string { b, _ := json.Marshal(out["result"]); return string(b) }
	readCall := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"read","arguments":{"path":` + jsonQuote(p) + `}}}`

	for _, c := range []struct {
		client string
		full   bool
	}{{"Perplexity", true}, {"claude-ai", false}} {
		w, _ := post(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"`+c.client+`","version":"1"}}}`, "", "test-agent")
		sid := w.Header().Get("Mcp-Session-Id")
		if sid == "" {
			t.Fatalf("%s: no Mcp-Session-Id handed out", c.client)
		}
		_, list := post(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, sid, "test-agent")
		steering := strings.Contains(toolsText(list), "instead of screenshots")
		if steering == c.full {
			t.Errorf("%s: token-saving wording present = %v", c.client, steering)
		}
		_, res := post(readCall, sid, "test-agent")
		whole := strings.Contains(toolsText(res), "THE END")
		if whole != c.full {
			t.Errorf("%s: whole file returned = %v, want %v", c.client, whole, c.full)
		}
	}
	if b, err := os.ReadFile(serveLogPath()); err == nil && !strings.Contains(string(b), `client "Perplexity"`) {
		t.Error("the client name must be logged in serve.log")
	}
}

func jsonQuote(s string) string { b, _ := json.Marshal(s); return string(b) }
