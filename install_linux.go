//go:build linux

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func configHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config")
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// linuxClaudeConfigPaths: the Claude desktop app's config on Linux (native or Flatpak builds).
func linuxClaudeConfigPaths() []string {
	h, _ := os.UserHomeDir()
	std := filepath.Join(configHome(), "Claude", "claude_desktop_config.json")
	var out []string
	if fi, err := os.Stat(filepath.Dir(std)); err == nil && fi.IsDir() {
		out = append(out, std)
	}
	flat, _ := filepath.Glob(filepath.Join(h, ".var", "app", "*", "config", "Claude"))
	for _, d := range flat {
		out = append(out, filepath.Join(d, "claude_desktop_config.json"))
	}
	if len(out) == 0 {
		out = append(out, std)
	}
	return out
}

func claudeCodeJSON() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude.json")
}

// linuxConnectClaudeCode registers TokenSaver for Claude Code (user scope): with the
// claude CLI when it's installed, else straight in ~/.claude.json. Returns 1 when connected.
func linuxConnectClaudeCode(target string) int {
	if cli, err := exec.LookPath("claude"); err == nil {
		if out, err := exec.Command(cli, "mcp", "get", "tokensaver").CombinedOutput(); err == nil && bytes.Contains(out, []byte(target)) {
			fmt.Println("Already connected to Claude Code")
			return 1
		}
		exec.Command(cli, "mcp", "remove", "--scope", "user", "tokensaver").Run()
		if out, err := exec.Command(cli, "mcp", "add", "--scope", "user", "tokensaver", "--", target, "mcp").CombinedOutput(); err != nil {
			fmt.Println("Couldn't connect Claude Code:", strings.TrimSpace(string(out)))
			return 0
		}
		fmt.Println("Connected to Claude Code")
		return 1
	}
	p := claudeCodeJSON()
	if !exists(p) {
		return 0
	}
	if configHasEntry(p, target) {
		fmt.Println("Already connected to Claude Code in", p)
		return 1
	}
	if err := editClaudeConfig(p, func(s map[string]any) {
		s["tokensaver"] = map[string]any{"type": "stdio", "command": target, "args": []string{"mcp"}}
	}); err != nil {
		fmt.Println("Couldn't update", p+":", err)
		return 0
	}
	fmt.Println("Connected to Claude Code in", p)
	return 1
}

func linuxDisconnectClaudeCode() {
	if cli, err := exec.LookPath("claude"); err == nil {
		if exec.Command(cli, "mcp", "remove", "--scope", "user", "tokensaver").Run() == nil {
			fmt.Println("Disconnected from Claude Code")
		}
		return
	}
	p := claudeCodeJSON()
	if b, err := os.ReadFile(p); err == nil && bytes.Contains(b, []byte(`"tokensaver"`)) {
		if editClaudeConfig(p, func(s map[string]any) { delete(s, "tokensaver") }) == nil {
			fmt.Println("Disconnected from Claude Code in", p)
		}
	}
}

// linuxInstallExtras: a "tokensaver" command on PATH, an app-menu entry and an
// "Open TokenSaver" launcher in the Saved Jobs folder.
func linuxInstallExtras(target string) {
	h, _ := os.UserHomeDir()
	bin := filepath.Join(h, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	link := filepath.Join(bin, "tokensaver")
	if cur, err := os.Readlink(link); err != nil || cur != target {
		if fi, err := os.Lstat(link); err == nil && fi.Mode()&os.ModeSymlink == 0 {
			os.Rename(link, link+".old."+time.Now().Format("20060102150405"))
		} else {
			os.Remove(link)
		}
		if os.Symlink(target, link) == nil {
			fmt.Println("Command: tokensaver →", target)
		}
	}
	apps := filepath.Join(h, ".local", "share", "applications")
	os.MkdirAll(apps, 0o755)
	desk := "[Desktop Entry]\nType=Application\nName=TokenSaver\nComment=Claude's hands on this PC\n" +
		"Exec=" + desktopExecQuote(target) + "\nTerminal=true\nIcon=utilities-terminal\nCategories=Utility;\n"
	os.WriteFile(filepath.Join(apps, "tokensaver.desktop"), []byte(desk), 0o644)
	os.MkdirAll(jobsFolder(), 0o755)
	os.WriteFile(filepath.Join(jobsFolder(), "Open TokenSaver.sh"), []byte("#!/bin/sh\nexec "+shQuote(target)+"\n"), 0o755)
}

// desktopExecQuote quotes a path for a .desktop Exec= line.
func desktopExecQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\\\`, `"`, `\"`, "`", "\\`", "$", `\$`)
	return `"` + r.Replace(s) + `"`
}

// writeLinuxShortcut: an executable script in Saved Jobs that runs the job in a terminal.
func writeLinuxShortcut(name string) (string, error) {
	exe := installPath()
	if !exists(exe) {
		exe, _ = os.Executable()
	}
	if err := os.MkdirAll(jobsFolder(), 0o755); err != nil {
		return "", err
	}
	p := shortcutPath(name)
	content := "#!/bin/sh\n# TokenSaver saved job\nexec " + shQuote(exe) + " run " + shQuote(name) + "\n"
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		return "", err
	}
	return p, nil
}
