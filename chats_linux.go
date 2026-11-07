//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The 6-hourly Claude chats sync is a systemd user timer (a scheduled task on Windows).

const chatsTimer = "tokensaver-chats.timer"

func chatsTaskState() string {
	if st, _ := userctl("is-enabled", chatsTimer); st == "enabled" {
		return "every 6 hours (systemd timer " + chatsTimer + ")"
	}
	return "not set up (chats action schedule)"
}

func chatsSchedule() (string, error) {
	sc, err := chatsShortcut()
	if err != nil {
		return "", fmt.Errorf("shortcut: %v", err)
	}
	exe := installPath()
	if !exists(exe) {
		exe, _ = os.Executable()
	}
	svc := "[Unit]\nDescription=TokenSaver: sync Claude chats to Markdown\n\n[Service]\nType=oneshot\nExecStart=" + systemdQuote(exe) + " chats sync\n"
	tmr := "[Unit]\nDescription=TokenSaver: sync Claude chats every 6 hours\n\n[Timer]\nOnBootSec=15min\nOnUnitActiveSec=6h\nPersistent=true\n\n[Install]\nWantedBy=timers.target\n"
	os.MkdirAll(userUnitDir(), 0o755)
	if err := os.WriteFile(filepath.Join(userUnitDir(), "tokensaver-chats.service"), []byte(svc), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(userUnitDir(), chatsTimer), []byte(tmr), 0o644); err != nil {
		return "", err
	}
	for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", chatsTimer}} {
		if out, err := userctl(a...); err != nil {
			return "Shortcut: " + sc, fmt.Errorf("systemctl --user %s: %s", strings.Join(a, " "), out)
		}
	}
	return "Scheduled: Claude chats sync every 6 hours (systemd timer " + chatsTimer + ").\nShortcut: " + sc, nil
}
