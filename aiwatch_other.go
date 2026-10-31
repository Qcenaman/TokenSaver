//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

// startAIWatcher starts "tokensaver ai-watch" in its own session so it outlives this process.
func startAIWatcher() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	c := exec.Command(exe, "ai-watch")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if c.Start() == nil {
		c.Process.Release()
	}
}
