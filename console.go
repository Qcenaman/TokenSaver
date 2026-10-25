package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var stdin = bufio.NewReader(os.Stdin)

func prompt(q string) string {
	fmt.Print(q)
	s, _ := stdin.ReadString('\n')
	return strings.TrimSpace(s)
}

func askYes(q string, def bool) bool {
	a := strings.ToLower(prompt(q))
	if a == "" {
		return def
	}
	return a == "y" || a == "yes"
}

func waitEnter(msg string, timeout time.Duration) {
	fmt.Print(msg)
	ch := make(chan struct{})
	go func() { stdin.ReadString('\n'); close(ch) }()
	if timeout <= 0 {
		<-ch
		return
	}
	select {
	case <-ch:
	case <-time.After(timeout):
	}
}

func installPath() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Claudessistent", "claudessistent.exe")
}

func consoleMenu() {
	if !isWin {
		fmt.Println("usage: claudessistent mcp | run <saved job> | version")
		return
	}
	exe, _ := os.Executable()
	if !samePath(exe, installPath()) {
		fmt.Printf("Claudessistent %s\n\n", appVersion)
		fmt.Println("This installs Claudessistent for your Windows account and connects it to the")
		fmt.Println("Claude desktop app, so Claude can hand it file jobs instead of doing every step itself.")
		fmt.Println()
		if askYes("Install now? [Y/n] ", true) {
			consoleInstall()
		}
		waitEnter("\nPress Enter to close.", 0)
		return
	}
	for {
		fmt.Printf("\nClaudessistent %s\n", appVersion)
		list := listSaved()
		if len(list) == 0 {
			fmt.Println("No saved jobs yet. Ask Claude to save one.")
		} else {
			fmt.Println("Saved jobs:")
			for i, s := range list {
				fmt.Printf("  %d. %s — %s\n", i+1, s.Name, clip(describeSteps(s.Steps), 90))
			}
		}
		fmt.Println("\nType a number to run a job · r = reconnect to Claude · u = disconnect from Claude · Enter = quit")
		a := strings.ToLower(prompt("> "))
		switch a {
		case "":
			return
		case "r":
			consoleInstall()
		case "u":
			consoleUninstall()
		default:
			if n, err := strconv.Atoi(a); err == nil && n >= 1 && n <= len(list) {
				consoleRunSaved(list[n-1].Name)
			}
		}
	}
}

func consoleRunSaved(name string) int {
	sj, err := loadSaved(name)
	if err != nil {
		fmt.Println(err)
		waitEnter("Press Enter to close.", 0)
		return 1
	}
	fmt.Printf("Claudessistent — %s\n%s\n\n", sj.Name, describeSteps(sj.Steps))
	if hasRisky(sj.Steps) {
		fmt.Println("This job deletes, renames or replaces files. Checking what it would do…")
		s := runConsoleJob(JobSpec{Name: sj.Name, Kind: "files", Steps: sj.Steps, DryRun: true, Source: "you"})
		fmt.Println(formatState(s))
		if s.Status != "done" {
			waitEnter("\nPress Enter to close.", 0)
			return 1
		}
		if !askYes("\nGo ahead? [y/N] ", false) {
			return 1
		}
	}
	s := runConsoleJob(JobSpec{Name: sj.Name, Kind: "files", Steps: sj.Steps, Source: "you"})
	fmt.Println(formatState(s))
	ok := s.Status == "done"
	for _, r := range s.Steps {
		if r.ErrorCount > 0 {
			ok = false
		}
	}
	if ok {
		waitEnter("\nDone. This window closes in 15 seconds (or press Enter).", 15*time.Second)
		return 0
	}
	waitEnter("\nPress Enter to close.", 0)
	return 1
}

func runConsoleJob(spec JobSpec) *JobState {
	j, err := newJob(spec)
	if err != nil {
		return &JobState{Status: "failed", Error: err.Error()}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	j.start()
	t := time.NewTicker(400 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-sig:
			fmt.Print("\rStopping…" + strings.Repeat(" ", 100))
			j.cancel()
		case <-j.done:
			fmt.Print("\r" + strings.Repeat(" ", 110) + "\r")
			s := j.snapshot()
			return &s
		case <-t.C:
			s := j.snapshot()
			line := progressLine(&s)
			if s.Current != "" {
				line += " · " + filepath.Base(s.Current)
			}
			fmt.Printf("\r%-110s", clip(line, 109))
		}
	}
}

func claudeConfigPaths() []string {
	appdata := os.Getenv("APPDATA")
	std := filepath.Join(appdata, "Claude", "claude_desktop_config.json")
	var out []string
	if dirs, _ := filepath.Glob(filepath.Join(os.Getenv("LOCALAPPDATA"), "Packages", "Claude_*", "LocalCache", "Roaming", "Claude")); len(dirs) > 0 {
		for _, d := range dirs {
			out = append(out, filepath.Join(d, "claude_desktop_config.json"))
		}
	}
	if fi, err := os.Stat(filepath.Dir(std)); (err == nil && fi.IsDir()) || len(out) == 0 {
		out = append(out, std)
	}
	return out
}

func editClaudeConfig(path string, edit func(servers map[string]any)) error {
	m := map[string]any{}
	b, err := os.ReadFile(path)
	if err == nil && len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s isn't valid JSON, so I left it untouched", path)
		}
		os.WriteFile(path+".bak", b, 0o644)
	}
	servers, _ := m["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	edit(servers)
	m["mcpServers"] = servers
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func consoleInstall() {
	target := installPath()
	exe, _ := os.Executable()
	if !samePath(exe, target) {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			fmt.Println("Couldn't create", filepath.Dir(target)+":", err)
			return
		}
		if exists(target) {
			old := target + ".old"
			os.Remove(old)
			if err := os.Rename(target, old); err != nil {
				fmt.Println("Couldn't replace the installed copy (is it in use?):", err)
				return
			}
		}
		if err := copyPlain(exe, target); err != nil {
			fmt.Println("Couldn't copy the app:", err)
			return
		}
		fmt.Println("Installed to", target)
	}
	connected := 0
	var todo []string
	for _, p := range claudeConfigPaths() {
		if configHasEntry(p, target) {
			fmt.Println("Already connected to Claude in", p)
			connected++
			continue
		}
		todo = append(todo, p)
	}
	if len(todo) > 0 && claudeRunning() {
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
			s["claudessistent"] = map[string]any{"command": target, "args": []string{"mcp"}}
		})
		if err != nil {
			fmt.Println("Couldn't update", p+":", err)
			continue
		}
		fmt.Println("Connected to Claude in", p)
		connected++
	}
	if connected == 0 {
		fmt.Printf("\nAdd this to the \"mcpServers\" section of Claude's claude_desktop_config.json by hand:\n  \"claudessistent\": {\"command\": %q, \"args\": [\"mcp\"]}\n", target)
	}
	os.MkdirAll(jobsFolder(), 0o755)
	os.WriteFile(filepath.Join(jobsFolder(), "Open Claudessistent.cmd"), []byte("@\""+target+"\"\r\n"), 0o644)
	fmt.Println("\nAll set. Open Claude (if it's open, quit it from the tray icon and reopen it) so it loads this version.")
	fmt.Println("Saved jobs will appear in the \"Claudessistent Jobs\" folder on your Desktop.")
}

func consoleUninstall() {
	for _, p := range claudeConfigPaths() {
		if !exists(p) {
			continue
		}
		if err := editClaudeConfig(p, func(s map[string]any) { delete(s, "claudessistent") }); err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println("Disconnected from Claude in", p)
	}
	fmt.Println("Restart Claude to finish. Your saved jobs and logs are kept in", appHome)
}

func copyPlain(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func configHasEntry(path, target string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var m struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	e, ok := m.MCPServers["claudessistent"]
	return ok && samePath(e.Command, target) && len(e.Args) == 1 && e.Args[0] == "mcp"
}
