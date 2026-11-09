//go:build linux

package main

import (
	"os"
	"testing"
)

func TestFillSessionEnv(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	fillSessionEnv(1000, func(p string) bool { return p == "/run/user/1000" || p == "/run/user/1000/bus" })
	if got := os.Getenv("XDG_RUNTIME_DIR"); got != "/run/user/1000" {
		t.Fatalf("XDG_RUNTIME_DIR = %q", got)
	}
	if got := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); got != "unix:path=/run/user/1000/bus" {
		t.Fatalf("DBUS_SESSION_BUS_ADDRESS = %q", got)
	}
}

func TestFillSessionEnvKeepsExistingAndRoot(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/custom")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/custom/bus2")
	fillSessionEnv(1000, func(string) bool { return true })
	if os.Getenv("XDG_RUNTIME_DIR") != "/custom" || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "unix:path=/custom/bus2" {
		t.Fatal("existing session variables were changed")
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	fillSessionEnv(0, func(string) bool { return true })
	if os.Getenv("XDG_RUNTIME_DIR") != "" {
		t.Fatal("root got a user session")
	}
}

func TestFillSessionEnvNoSession(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "")
	fillSessionEnv(1000, func(string) bool { return false })
	if os.Getenv("XDG_RUNTIME_DIR") != "" || os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
		t.Fatal("set variables for a session that does not exist")
	}
}
