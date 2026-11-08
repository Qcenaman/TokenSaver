package main

// Local mode: the same TokenSaver serving a local model (AnythingLLM, LM Studio, Cline)
// the way ContextSaver did: one small "pc" tool with short descriptions and help on
// demand (compact.go), approvals for risky calls (approvals.go), model switching
// (model.go) and ContextSaver's own data folder, so existing setups keep working.
// On when the program is named contextsaver*, TOKENSAVER_MODE=local, or with
// "mcp --local" / "mcp --for <client>".

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var localMode = detectLocalMode(os.Args)

func detectLocalMode(args []string) bool {
	if strings.EqualFold(os.Getenv("TOKENSAVER_MODE"), "local") {
		return true
	}
	if len(args) > 0 && strings.HasPrefix(strings.ToLower(args[0][strings.LastIndexAny(args[0], `/\`)+1:]), "contextsaver") {
		return true
	}
	for i, a := range args {
		if i > 0 && (a == "--local" || a == "--for") {
			return true
		}
	}
	return false
}

// appName: how the program calls itself in messages and to MCP clients.
func appName() string {
	if localMode {
		return "contextsaver"
	}
	return "tokensaver"
}

const localInstructions = `Use the pc tool for anything on this PC; help{c} shows a command's args.
Any GitHub repo's code (his own: Qcenaman/<name>): owner/name[@ref][/sub] as path in find/read/ls/outline/git compare{path,to}; never fetch or web-search it another way.`

// platformLocal adjusts the local tool's text for Linux.
func platformLocal(s string) string {
	if isWin {
		return s
	}
	return strings.NewReplacer("Windows PC", "Linux PC", `D:\x.txt`, "/home/x.txt", `D:\Claude Apps`, "~/Claude Apps").Replace(s)
}

func toRaw(a map[string]any) json.RawMessage {
	b, _ := json.Marshal(a)
	return b
}

// repoRun lets a local-mode command take owner/name[@ref][/sub] as its path, through
// TokenSaver's temporary repo copies (repos.go).
func repoRun(tool string, a map[string]any, f func() (string, error)) (string, error) {
	raw, fix, err := repoArgs(toRaw(a), "path")
	if err != nil {
		return "", err
	}
	if fix != nil {
		m := map[string]any{}
		json.Unmarshal(raw, &m)
		for k := range a {
			delete(a, k)
		}
		for k, v := range m {
			a[k] = v
		}
	}
	out, err := f()
	if fix != nil {
		out = fix(out)
	}
	return out, err
}

// repoQuestion: TokenSaver keeps repo copies current by itself, so nothing to ask.
func repoQuestion(c string, a map[string]any) string { return "" }

func startRepoJanitor() { go repoSweep() }

func lookOK(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}



// localRouteIP: the PC's IPv4 on the interface that reaches target.
func localRouteIP(target string) string {
	conn, err := net.Dial("udp", net.JoinHostPort(target, "9"))
	if err != nil {
		return ""
	}
	defer conn.Close()
	if ip := conn.LocalAddr().(*net.UDPAddr).IP.To4(); ip != nil {
		return ip.String()
	}
	return ""
}

// mcpSend writes a message to the client (set while serving).
var mcpSend func(any)

// progressFunc sends notifications/progress for a call that sent a progress token
// (clients reset their tool-call timeout on progress); nil without a token.
func progressFunc(tok any) func(string) {
	if tok == nil || mcpSend == nil {
		return nil
	}
	n := 0
	return func(msg string) {
		n++
		mcpSend(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{"progressToken": tok, "progress": n, "message": msg}})
	}
}

// toolOutlineMap: outline with map arguments (local-mode commands).
func toolOutlineMap(a map[string]any) (string, error) { return toolOutline(toRaw(a)) }

// tokenSaverRoot: TokenSaver's own folder (the shared index lives there), in both modes.
func tokenSaverRoot() string {
	if v := os.Getenv("TOKENSAVER_ROOT"); v != "" {
		return v
	}
	if isWin {
		return `D:\Claude Apps\TokenSaver`
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Claude Apps", "TokenSaver")
}
