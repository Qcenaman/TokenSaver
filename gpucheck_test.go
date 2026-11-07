package main

import (
	"strings"
	"testing"
)

func TestGPUGateContinueAndNoLoad(t *testing.T) {
	if r, n := gpuGate("The local AI", "continue", true); r != "" || n != "" {
		t.Fatalf("continue must never block: %q %q", r, n)
	}
	if r, n := gpuGate("The local AI", "", false); r != "" || n != "" {
		t.Fatalf("nothing to load must never block: %q %q", r, n)
	}
}

func TestGPUModeKeepsOldForce(t *testing.T) {
	if gpuMode("", true) != "continue" || gpuMode("stop", true) != "stop" || gpuMode("", false) != "" {
		t.Fatal("force:true must mean continue unless gpu is set")
	}
}

func TestDescribeGPUUsersAndNote(t *testing.T) {
	d := describeGPUUsers([]*gpuUser{{what: "ComfyUI", pids: []int{1, 2, 3, 4}}}, "GPU has 9.0 of 24.0 GB VRAM in use and is 80% busy")
	if !strings.Contains(d, "ComfyUI is running (pid 1, 2, 3, …)") || !strings.Contains(d, "VRAM") {
		t.Fatalf("got %q", d)
	}
	if gpuNoted("", "x") != "x" || gpuNoted("n", "x") != "n\nx" {
		t.Fatal("gpuNoted")
	}
}
