package main

// Install, uninstall and the double-click menu in local mode: connect to AnythingLLM and
// LM Studio (what ContextSaver did) instead of Claude.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var _ = strings.TrimSpace
var _ = time.Now

func localConsoleMenu() {
	exe, _ := os.Executable()
	if !samePath(exe, localInstallPath()) {
		fmt.Printf("ContextSaver %s for Linux\n\n", appVersion)
		fmt.Println("This installs ContextSaver for your Linux account and connects it to AnythingLLM")
		fmt.Println("and LM Studio, so your local AI can use this PC with as little context as possible.")
		fmt.Println()
		if askYes("Install now? [Y/n] ", true) {
			localConsoleInstall()
		}
		waitEnter("\nPress Enter to close.", 0)
		return
	}
	terminal()
}

func localConsoleInstall() {
	migrateOldInstall()
	target := localInstallPath()
	exe, _ := os.Executable()
	if !samePath(exe, target) {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			fmt.Println("Couldn't create", filepath.Dir(target)+":", err)
			return
		}
		if exists(target) {
			old := target + ".old"
			if exists(old) {
				keep := filepath.Join(appRoot(), "old", "ContextSaver program")
				os.MkdirAll(keep, 0o755)
				os.Rename(old, filepath.Join(keep, "contextsaver.old."+time.Now().Format("20060102150405")))
			}
			if err := os.Rename(target, old); err != nil {
				fmt.Println("Couldn't replace the installed copy (is it in use?):", err)
				return
			}
		}
		if err := copyPlain(exe, target); err != nil {
			fmt.Println("Couldn't copy the app:", err)
			return
		}
		os.Chmod(target, 0o755)
		fmt.Println("Installed to", target)
	}
	linkOnPath(target)
	connected := 0
	var todo []string
	for _, p := range anythingConfigPaths() {
		if configHasEntry(p, target) {
			fmt.Println("Already connected to AnythingLLM in", p)
			connected++
			continue
		}
		todo = append(todo, p)
	}
	if false {
		fmt.Println("\nClaude is open. It rewrites its settings when it closes, which would undo this.")
		for claudeRunning() {
			a := prompt("Quit Claude completely (right-click its icon near the clock → Quit), then press Enter. Type s to skip: ")
			if strings.EqualFold(a, "s") {
				todo = nil
				break
			}
		}
	}
	for _, p := range todo {
		err := editClaudeConfig(p, func(s map[string]any) {
			// ContextSaver replaces these, whose tool lists fill a local model's context.
			delete(s, "tokensaver")
			delete(s, "windows-mcp")
			s["contextsaver"] = map[string]any{"command": target, "args": []string{"mcp"}}
		})
		if err != nil {
			fmt.Println("Couldn't update", p+":", err)
			continue
		}
		fmt.Println("Connected to AnythingLLM in", p)
		connected++
	}
	if p, err := connectLMStudio(target); err != nil {
		fmt.Println("Couldn't update", p+":", err)
	} else if p != "" {
		fmt.Println("Connected to LM Studio in", p)
		connected++
	}
	if connected == 0 && len(anythingConfigPaths()) == 0 && lmStudioMCPPath() == "" {
		fmt.Println("No AnythingLLM or LM Studio here: chat with the local model through a \"Local AI - <model>\" icon (ContextSaver Chat).")
	} else if connected == 0 {
		fmt.Printf("\nAdd this to the \"mcpServers\" section of AnythingLLM's anythingllm_mcp_servers.json or LM Studio's mcp.json by hand:\n  \"contextsaver\": {\"command\": %q, \"args\": [\"mcp\"]}\n", target)
	}
	refreshHelper()
	os.MkdirAll(jobsFolder(), 0o755)
	writeLauncher(filepath.Join(jobsFolder(), "Open ContextSaver.desktop"), "Open ContextSaver", "ContextSaver's terminal", "", []string{target}, true, 0)
	writeModelShortcuts(target)
	if n := syncFrontends(); n != "" {
		fmt.Println(strings.TrimPrefix(n, "; "))
	}
	writeDesktopShortcuts(target)
	if isTerminal(os.Stdin) && setupNeeded() {
		if askYes("\nInstall the programs ContextSaver uses (llama.cpp, GitHub CLI, compilers, OCR...)? It asks before each change. [Y/n] ", true) {
			consoleLocalSetup()
		}
	}
	if !helperInstalled() {
		fmt.Println("\n⚠ " + helperHowTo())
	}
	if len(anythingConfigPaths()) > 0 {
		fmt.Println("\nAll set. In AnythingLLM open Agent Skills and refresh the MCP servers (or restart AnythingLLM).")
	} else {
		fmt.Println("\nAll set. Double-click a \"Local AI - <model>\" icon on the Desktop to chat.")
	}
	fmt.Println("Everything ContextSaver keeps is in", appRoot())
}

func localConsoleUninstall() {
	for _, p := range anythingConfigPaths() {
		if !exists(p) {
			continue
		}
		if err := editClaudeConfig(p, func(s map[string]any) { delete(s, "contextsaver"); delete(s, "claudessistent") }); err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println("Disconnected from AnythingLLM in", p)
	}
	if p, err := disconnectLMStudio(); err != nil {
		fmt.Println(err)
	} else if p != "" {
		fmt.Println("Disconnected from LM Studio in", p)
	}
	fmt.Println("Restart AnythingLLM or LM Studio to finish. Your saved jobs and logs are kept in", appHome)
}

// anythingConfigPaths lists AnythingLLM Desktop's MCP server files (none when it isn't installed).
func anythingConfigPaths() []string {
	if !exists(anythingStorage()) && !exists(anythingLLMExe()) {
		return nil
	}
	return []string{filepath.Join(anythingStorage(), "plugins", "anythingllm_mcp_servers.json")}
}

func localInstallPath() string {
	return filepath.Join(appRoot(), "app", "contextsaver")
}

// linkOnPath puts a contextsaver command in ~/.local/bin (on Ubuntu's PATH), so the
// hints "run: contextsaver ..." work in any terminal.
func linkOnPath(target string) {
	h, _ := os.UserHomeDir()
	l := filepath.Join(h, ".local", "bin", "contextsaver")
	if cur, err := os.Readlink(l); err == nil && cur == target {
		return
	}
	os.MkdirAll(filepath.Dir(l), 0o755)
	os.Remove(l)
	if os.Symlink(target, l) == nil {
		fmt.Println("Command: contextsaver (in ~/.local/bin)")
	}
}
