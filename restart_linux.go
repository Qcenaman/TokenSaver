//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// claudeMain finds the Claude desktop app's main process (Electron children carry --type=).
func claudeMain() (pid int, exe string, args []string) {
	ps, _ := procs()
	for _, p := range ps {
		n := strings.ToLower(p.name)
		if n != "claude" && n != "claude-desktop" && !strings.Contains(strings.ToLower(p.cmd), "/claude-desktop/") {
			continue
		}
		if strings.Contains(p.cmd, "--type=") || strings.Contains(p.cmd, " mcp") {
			continue
		}
		e, err := os.Readlink("/proc/" + itoa(p.pid) + "/exe")
		if err != nil {
			continue
		}
		b, _ := os.ReadFile("/proc/" + itoa(p.pid) + "/cmdline")
		a := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if len(a) > 0 {
			a = a[1:]
		}
		// Claude Code's CLI is also called "claude"; only Electron apps have resources next to them.
		if !exists(filepath.Join(filepath.Dir(e), "resources")) && !strings.Contains(p.cmd, "electron") {
			continue
		}
		return p.pid, e, a
	}
	return 0, "", nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// restartClaude quits the Claude desktop app and starts it again from a detached
// "tokensaver restart-claude" (this process is Claude's child and dies with it).
func restartClaude() (string, error) {
	pid, _, _ := claudeMain()
	if pid == 0 {
		return "", errors.New("the Claude desktop app isn't running")
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	c := exec.Command(self, "restart-claude")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return "", err
	}
	c.Process.Release()
	return "Restarting Claude desktop in a few seconds; this chat ends. Start a new chat after it reopens.", nil
}

func restartClaudeNow() int {
	time.Sleep(2 * time.Second)
	pid, exe, args := claudeMain()
	if pid == 0 {
		return 1
	}
	syscall.Kill(pid, syscall.SIGTERM)
	for i := 0; i < 75 && pidAlive(pid); i++ {
		time.Sleep(200 * time.Millisecond)
	}
	if pidAlive(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		time.Sleep(time.Second)
	}
	c := exec.Command(exe, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Start() != nil {
		return 1
	}
	c.Process.Release()
	return 0
}
