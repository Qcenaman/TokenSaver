//go:build windows

package main

import (
	"fmt"
	"os"
	"strings"
)

const chatsTaskName = `TokenSaver\Chats Sync`

func chatsTaskState() string {
	if quietCmd("schtasks", "/Query", "/TN", chatsTaskName).Run() == nil {
		return "every 6 hours (task \"" + chatsTaskName + "\")"
	}
	return "not set up (chats action schedule)"
}

// chatsSchedule registers the 6-hourly sync (hidden: conhost --headless) through the elevated
// helper, and writes the double-click shortcut.
func chatsSchedule() (string, error) {
	sc, err := chatsShortcut()
	if err != nil {
		return "", fmt.Errorf("shortcut: %v", err)
	}
	exe := installPath()
	if !exists(exe) {
		exe, _ = os.Executable()
	}
	if !isElevated() {
		if !pipeUp() {
			return "Shortcut: " + sc, fmt.Errorf("the elevated helper isn't running; start it, then schedule again")
		}
		var out strings.Builder
		ep, err := startElevated(elevRequest{Command: `"` + exe + `" chats schedule`, Shell: "cmd", Confirmed: true}, &out)
		if err != nil {
			return "Shortcut: " + sc, err
		}
		<-ep.done
		if ep.err != nil {
			return "Shortcut: " + sc, ep.err
		}
		return strings.TrimSpace(out.String()) + "\nShortcut: " + sc, nil
	}
	tr := `conhost.exe --headless "` + exe + `" chats sync`
	out, err := quietCmd("schtasks", "/Create", "/F", "/TN", chatsTaskName, "/SC", "HOURLY", "/MO", "6", "/TR", tr, "/RL", "LIMITED").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("schtasks: %s", strings.TrimSpace(string(out)))
	}
	return "Scheduled: Claude chats sync every 6 hours (task \"" + chatsTaskName + "\").", nil
}
