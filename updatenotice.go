package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The installed program can be replaced while this process (an mcp server started by
// Claude desktop) keeps running the old code. Tool results then start with a note
// saying so, instead of silently answering from the old version.
var (
	updMu      sync.Mutex
	updStart   os.FileInfo // the installed program when this process started
	updChecked time.Time
	updNote    string
)

func initUpdateNotice() {
	updMu.Lock()
	defer updMu.Unlock()
	if fi, err := os.Stat(installPath()); err == nil {
		updStart = fi
	}
}

// updateNotice is "" while the installed program is the one this process runs; once a
// newer version is installed it is a one-line note to put in front of tool results.
// The check costs one stat, at most every 10 seconds.
func updateNotice() string {
	updMu.Lock()
	defer updMu.Unlock()
	if updNote != "" || updStart == nil || time.Since(updChecked) < 10*time.Second {
		return updNote
	}
	updChecked = time.Now()
	fi, err := os.Stat(installPath())
	if err != nil || (fi.Size() == updStart.Size() && fi.ModTime().Equal(updStart.ModTime())) {
		return ""
	}
	v := installedVersion()
	if v == appVersion {
		updStart = fi // same version built again: nothing to load
		return ""
	}
	updNote = updateNoteFor(v)
	return updNote
}

func updateNoteFor(v string) string {
	if v == "" {
		v = "a newer version"
	}
	return "NOTE: TokenSaver updated to " + v + " but this session still runs " + appVersion +
		" — restart Claude desktop to load it (system action restart_claude does that)."
}

// installedVersion reads the version of the installed program: the version.txt install
// writes next to it, or, failing that, the program's own answer to "version".
func installedVersion() string {
	exe := installPath()
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "version.txt")); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			return v
		}
	}
	out, err := runQuiet(10*time.Second, exe, "version")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "tokensaver"))
}