//go:build !windows

package main

import (
	"errors"
	"time"
)

func inServiceSession() bool { return false }

func runInUserSession(exe string, args []string, dir string, wait time.Duration) (int, error) {
	return -1, errors.New("only needed on Windows")
}
