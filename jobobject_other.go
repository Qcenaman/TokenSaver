//go:build !windows

package main

import "os/exec"

func prepareChildInJob(cmd *exec.Cmd) {}

func putChildInJob(pid int) error { return nil }
