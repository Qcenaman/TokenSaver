//go:build !windows

package main

import (
	"errors"
	"time"
)

func inServiceSession() bool { return false }

func activeUserSession() (uint32, bool) { return 0, true }

func currentSessionID() uint32 { return 0 }

func runInUserSession(exe string, args []string, dir string, wait time.Duration) (int, error) {
	return -1, errors.New("only needed on Windows")
}
