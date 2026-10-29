//go:build !windows

package main

import "errors"

func restartClaude() (string, error) {
	return "", errors.New("restarting Claude desktop is only done on Windows")
}

func restartClaudeNow() int { return 2 }