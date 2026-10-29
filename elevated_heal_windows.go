package main

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// isTokenSaverExe reports whether p is a TokenSaver program (by file name). It
// lets a stale TokenSaver helper from an old install path be told apart from an
// unrelated process that happens to hold the elevated pipe.
func isTokenSaverExe(p string) bool {
	b := strings.ToLower(filepath.Base(p))
	return b == "tokensaver.exe" || strings.HasPrefix(b, "tokensaver.exe.old")
}

// restartHelperTask ends and re-runs the installed helper task (no UAC: the task
// is already registered and points at the install path), so a helper still
// serving an old or archived exe is replaced by the installed program. True once
// the pipe is back up.
func restartHelperTask() bool {
	if !taskInstalled() {
		return false
	}
	quietCmd("schtasks", "/End", "/TN", elevTaskName).Run()
	time.Sleep(500 * time.Millisecond)
	quietCmd("schtasks", "/Run", "/TN", elevTaskName).Run()
	return waitInstalledHelper(10 * time.Second)
}

// waitInstalledHelper: true once the pipe is served by the installed program (not an
// old one that is still shutting down), waiting up to d for it.
func waitInstalledHelper(d time.Duration) bool {
	end := time.Now().Add(d)
	for {
		if pid, p := pipeServer(); pid != 0 && samePath(p, installPath()) {
			return true
		}
		if time.Now().After(end) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

var (
	healMu   sync.Mutex
	healedOK bool
)

// healHelper re-registers the elevated helper so its scheduled task points at the
// current install (one UAC prompt). This runs automatically when an admin command
// finds the helper still serving an old path after the program was updated or
// moved, so the user isn't forced to re-approve a prompt for every command. It
// succeeds at most once per process; if the prompt is declined it may run again
// on a later command.
func healHelper() bool {
	healMu.Lock()
	defer healMu.Unlock()
	if healedOK {
		return true
	}
	if !taskInstalled() {
		return false
	}
	if err := elevateSelf("install-helper"); err != nil {
		return false
	}
	healedOK = waitInstalledHelper(10 * time.Second)
	return healedOK
}
