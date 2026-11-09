//go:build linux

package main

import (
	"fmt"
	"os"
)

// fillSessionEnv: started outside a login session (Claude desktop's MCP server, a systemd
// service, ssh), TokenSaver has no XDG_RUNTIME_DIR or session bus, so "systemctl --user",
// notify-send and gio in run jobs fail with "Failed to connect to bus". Point them at the
// user's own session when it exists; root (the admin helper) is left alone.
func fillSessionEnv(uid int, exists func(string) bool) {
	if uid == 0 {
		return
	}
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		run = fmt.Sprintf("/run/user/%d", uid)
		if !exists(run) {
			return
		}
		os.Setenv("XDG_RUNTIME_DIR", run)
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && exists(run+"/bus") {
		os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+run+"/bus")
	}
}

func init() {
	fillSessionEnv(os.Getuid(), func(p string) bool { _, err := os.Stat(p); return err == nil })
}
