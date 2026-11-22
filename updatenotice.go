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
	syncVersionFile()
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

// installedVersion reads the version of the installed program: its own answer to
// "version" (always right, even when the exe was copied in by hand), or, failing that,
// the version.txt install writes next to it.
func installedVersion() string {
	exe := installPath()
	if out, err := runQuiet(10*time.Second, exe, "version"); err == nil {
		if v := parseVersionOutput(out); v != "" {
			return v
		}
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "version.txt")); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// parseVersionOutput turns "tokensaver 2.14.1 (windows)" into "2.14.1".
func parseVersionOutput(out string) string {
	f := strings.Fields(strings.TrimSpace(out))
	if len(f) >= 2 && f[0] == "tokensaver" {
		return f[1]
	}
	return ""
}

// syncVersionFile rewrites version.txt when the installed program is the one running and
// the file says another version (an exe copied in by hand never updates it).
func syncVersionFile() {
	exe, err := os.Executable()
	if err != nil || !samePath(exe, installPath()) {
		return
	}
	p := filepath.Join(filepath.Dir(exe), "version.txt")
	if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == appVersion {
		return
	}
	os.WriteFile(p, []byte(appVersion+"\n"), 0o644)
}
