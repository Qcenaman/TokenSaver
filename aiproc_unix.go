//go:build !windows

package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// The local AI server process on Linux, macOS and other Unix systems.

type aiProc struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func (p *aiProc) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func startScript(script string) (*aiProc, error) {
	cmd := exec.Command(shBin(), script)
	cmd.Dir = filepath.Dir(script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &aiProc{cmd: cmd, done: make(chan struct{})}
	go func() { cmd.Wait(); close(p.done) }()
	return p, nil
}

func stopLocalAI(p *aiProc, port, ownPid int, ownModel string) {
	if p.cmd.Process != nil {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}
