package main

// "No saving" mode: clients without a token limit (Perplexity) get whole outputs. The server marks
// their tool calls with an internal "_full_output" argument; a tool raises its defaults and
// ceilings only for marked calls, and an argument the client sent itself still wins. Every other
// client (Claude desktop, claude.ai, the phone, RemoteAI) gets exactly today's short results.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	fullOutputMarker     = "_full_output"
	fullOutputDefaultMax = 400000
	fullCountLimit       = 1 << 20 // lines, matches, list entries for marked calls
	fullScreenshotPx     = 1600
)

// fullOutputSettings re-reads config.json, so a changed list works without a release or restart.
func fullOutputSettings() (clients []string, maxChars int) {
	clients, maxChars = cfg.FullOutputClients, cfg.FullOutputMaxChars
	if appHome != "" {
		if b, err := os.ReadFile(filepath.Join(appHome, "config.json")); err == nil {
			var c struct {
				Clients  []string `json:"full_output_clients"`
				MaxChars int      `json:"full_output_max_chars"`
			}
			if json.Unmarshal(b, &c) == nil {
				if c.Clients != nil {
					clients = c.Clients
				}
				if c.MaxChars > 0 {
					maxChars = c.MaxChars
				}
			}
		}
	}
	if clients == nil {
		clients = []string{"perplexity"}
	}
	if maxChars <= 0 {
		maxChars = fullOutputDefaultMax
	}
	return clients, maxChars
}

func fullOutputMaxChars() int { _, m := fullOutputSettings(); return m }

// isFullOutputClient: the client name (clientInfo.name, or the User-Agent when there is none)
// contains one of full_output_clients.
func isFullOutputClient(names ...string) bool {
	clients, _ := fullOutputSettings()
	for _, n := range names {
		n = strings.ToLower(n)
		if n == "" {
			continue
		}
		for _, c := range clients {
			if c = strings.ToLower(strings.TrimSpace(c)); c != "" && strings.Contains(n, c) {
				return true
			}
		}
	}
	return false
}

var fullMarkerRe = regexp.MustCompile(`"` + fullOutputMarker + `"\s*:\s*true`)

// isFullCall tells whether a tool call's arguments carry the server's mark.
func isFullCall(raw json.RawMessage) bool { return fullMarkerRe.Match(raw) }

// markFullOutput adds the mark to a tool call's arguments.
func markFullOutput(args json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if len(bytes.TrimSpace(args)) == 0 || json.Unmarshal(args, &m) != nil || m == nil {
		m = map[string]json.RawMessage{}
	}
	m[fullOutputMarker] = json.RawMessage("true")
	out, err := json.Marshal(m)
	if err != nil {
		return args
	}
	return out
}

// markFullOutputRequest marks a tools/call request body (JSON-RPC) for a full-output client.
func markFullOutputRequest(body []byte) []byte {
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(req["params"], &params) != nil || params == nil {
		return body
	}
	params["arguments"] = markFullOutput(params["arguments"])
	pb, err := json.Marshal(params)
	if err != nil {
		return body
	}
	req["params"] = pb
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// outCap is a character limit: what the client asked for, else the tool's default; capped at the
// tool's ceiling, or at full_output_max_chars for marked calls (whose default is that ceiling).
func outCap(full bool, given, def, ceil int) int {
	if full {
		m := fullOutputMaxChars()
		if given > 0 {
			return min(given, m)
		}
		return m
	}
	if given <= 0 {
		given = def
	}
	return min(given, ceil)
}

// countCap is the same for counts (lines, matches, entries).
func countCap(full bool, given, def, ceil int) int {
	if full {
		if given > 0 {
			return given
		}
		return fullCountLimit
	}
	if given <= 0 {
		given = def
	}
	return min(given, ceil)
}

// ---------- sessions: which client is calling over HTTP ----------

// The claude.ai connector and Perplexity share one connector path, so the client is told apart by
// the clientInfo it sends on initialize, remembered under the Mcp-Session-Id handed out then
// (kept in data\mcp-sessions.json across restarts). Without a session the User-Agent decides.
type mcpSession struct {
	Client  string    `json:"client"`
	Version string    `json:"version,omitempty"`
	Agent   string    `json:"agent,omitempty"`
	Seen    time.Time `json:"seen"`
}

var mcpSessions struct {
	sync.Mutex
	m      map[string]mcpSession
	loaded bool
}

func mcpSessionsPath() string { return filepath.Join(appHome, "mcp-sessions.json") }

func loadMCPSessionsLocked() {
	if mcpSessions.loaded {
		return
	}
	mcpSessions.loaded = true
	mcpSessions.m = map[string]mcpSession{}
	if appHome == "" {
		return
	}
	if b, err := os.ReadFile(mcpSessionsPath()); err == nil {
		json.Unmarshal(b, &mcpSessions.m)
	}
}

// newMCPSession records a client's initialize and returns the session id to hand back.
func newMCPSession(client, version, agent string) string {
	id := randHex(16)
	mcpSessions.Lock()
	defer mcpSessions.Unlock()
	loadMCPSessionsLocked()
	for k, s := range mcpSessions.m { // forget sessions unused for a week
		if time.Since(s.Seen) > 7*24*time.Hour {
			delete(mcpSessions.m, k)
		}
	}
	mcpSessions.m[id] = mcpSession{Client: client, Version: version, Agent: clip(agent, 120), Seen: time.Now()}
	if appHome != "" {
		writeJSONAtomic(mcpSessionsPath(), mcpSessions.m)
	}
	return id
}

// mcpSessionClient is the client name behind a session id ("" when unknown).
func mcpSessionClient(id string) string {
	if id == "" {
		return ""
	}
	mcpSessions.Lock()
	defer mcpSessions.Unlock()
	loadMCPSessionsLocked()
	s, ok := mcpSessions.m[id]
	if !ok {
		return ""
	}
	if time.Since(s.Seen) > time.Hour { // refresh now and then, not on every call
		s.Seen = time.Now()
		mcpSessions.m[id] = s
		if appHome != "" {
			writeJSONAtomic(mcpSessionsPath(), mcpSessions.m)
		}
	}
	return s.Client
}

// ---------- wording ----------

// Token-saving steering is left out of what full-output clients read; functional guidance
// (run_job batching, build_app in one call, paths, screenshot / ocr / console) stays.
var fullOutputWording = []struct {
	re   *regexp.Regexp
	with string
}{
	// server instructions
	{regexp.MustCompile(` and returns short results, so use it`), `, so use it`},
	{regexp.MustCompile(`read for just the part you need`), `read to read files`},
	{regexp.MustCompile(`run for commands \(exit code and error lines only\)`), `run for commands (exit code and the whole output)`},
	{regexp.MustCompile(` instead of screenshots;`), `;`},
	// tool descriptions
	{regexp.MustCompile(`On-screen text instead of screenshots\. `), `On-screen text, screenshots and input. `},
	{regexp.MustCompile(`, so only the needed lines get read`), ``},
	{regexp.MustCompile(`Read part of a file: `), `Read a file (the whole file as text by default) or part of it: `},
	{regexp.MustCompile(`returns exit code, error lines and last lines \(tail; 0 = none\), full log stays on the PC; summary:true adds the local AI's few-line summary of a long output when the command has finished`),
		`returns exit code and the whole output (tail: only the last lines; summary:true adds the local AI's summary)`},
	{regexp.MustCompile(`, only when text isn't enough`), ``},
	{regexp.MustCompile(`\(max_px (1000|800)\)`), `(max_px 1600)`},
	{regexp.MustCompile(`\(max_px, default (1000|800)\)`), `(max_px, default 1600)`},
	{regexp.MustCompile(`Long text cut at max_chars \(4000\) and saved to a file; summary:true summarizes locally; `), `summary:true summarizes locally; `},
	{regexp.MustCompile(`returns matches with up to lines \(3\) matching lines per file and a count of the rest`), `returns matches with their matching lines (lines limits them)`},
	{regexp.MustCompile(`max_chars \(8000\)`), `max_chars`},
	{regexp.MustCompile(`max: issues shown \(40\)`), `max: issues shown`},
}

// fullOutputHelper tells clients that may not see images that the PC's own model can look for them.
const fullOutputHelper = "This PC has a local AI helper: to look at something use screenshot with a question (it answers in words); for a second opinion or to read many files ask local_ai."

var helperHint = map[string]bool{"local_ai": true, "window": true, "browser": true}

func fullOutputText(s string) string {
	for _, w := range fullOutputWording {
		s = w.re.ReplaceAllString(s, w.with)
	}
	return strings.TrimSpace(s)
}

// fullOutputToolDefs rewrites the tool descriptions for full-output clients; names, arguments
// and schemas stay the same.
func fullOutputToolDefs(defs []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(defs))
	for _, d := range defs {
		var m map[string]json.RawMessage
		var desc string
		if json.Unmarshal(d, &m) != nil || json.Unmarshal(m["description"], &desc) != nil {
			out = append(out, d)
			continue
		}
		var name string
		json.Unmarshal(m["name"], &name)
		desc = fullOutputText(desc)
		if helperHint[name] {
			desc += " " + fullOutputHelper
		}
		nb, err := json.Marshal(desc)
		if err != nil {
			out = append(out, d)
			continue
		}
		m["description"] = nb
		b, err := json.Marshal(m)
		if err != nil {
			out = append(out, d)
			continue
		}
		out = append(out, b)
	}
	return out
}

// ---------- re-sync notice ----------

// Perplexity keeps the tool list it read when the connector was added or last re-synced, so after
// an update it may still use old descriptions (e.g. "browser: text only"). The hash of the list
// each client last fetched is kept; while a full-output client works from an older list, its
// tool results carry one line telling the user how to re-sync.

func toolDefsHash(defs []json.RawMessage) string {
	h := sha256.New()
	for _, d := range defs {
		h.Write(d)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func toolSyncPath() string { return filepath.Join(appHome, "tool-sync.json") }

var toolSync sync.Mutex

func loadToolSync() map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(toolSyncPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func clientKey(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return "unknown"
	}
	return name
}

// noteToolsListed records the tool list a client just fetched.
func noteToolsListed(client string, defs []json.RawMessage) {
	if appHome == "" {
		return
	}
	toolSync.Lock()
	defer toolSync.Unlock()
	m := loadToolSync()
	m[clientKey(client)] = toolDefsHash(defs)
	writeJSONAtomic(toolSyncPath(), m)
}

// resyncNotice is the line for a full-output client still working from an older tool list.
func resyncNotice(client string) string {
	toolSync.Lock()
	m := loadToolSync()
	toolSync.Unlock()
	if m[clientKey(client)] == toolDefsHash(fullOutputToolDefs(platformToolDefs())) {
		return ""
	}
	return fmt.Sprintf("NOTE for the user: TokenSaver was updated (%s) and its tools changed, but this app still has the old tool list. "+
		"In the Perplexity app: Plugins > Installed > TokenSaver > More actions > Re-sync tools, then start a new chat.", appVersion)
}

// appendResultNote adds a line to a tools/call reply's first text block.
func appendResultNote(reply []byte, note string) []byte {
	var r map[string]any
	if json.Unmarshal(reply, &r) != nil {
		return reply
	}
	res, _ := r["result"].(map[string]any)
	content, _ := res["content"].([]any)
	if len(content) == 0 {
		return reply
	}
	first, _ := content[0].(map[string]any)
	if t, ok := first["text"].(string); ok {
		first["text"] = t + "\n\n" + note
	}
	out, err := json.Marshal(r)
	if err != nil {
		return reply
	}
	return out
}
