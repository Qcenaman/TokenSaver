//go:build !windows

package main

func chatsTaskState() string { return "not available on this system (use cron: tokensaver chats sync)" }

func chatsSchedule() (string, error) {
	sc, err := chatsShortcut()
	if err != nil {
		return "", err
	}
	return "Shortcut: " + sc + "\nSchedule it with cron: tokensaver chats sync", nil
}
