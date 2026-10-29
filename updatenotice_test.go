package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstalledVersionFromFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENSAVER_ROOT", root)
	app := filepath.Join(root, "app")
	os.MkdirAll(app, 0o755)
	os.WriteFile(filepath.Join(app, "version.txt"), []byte(" 9.9.9 \n"), 0o644)
	if v := installedVersion(); v != "9.9.9" {
		t.Fatalf("got %q", v)
	}
}

func TestUpdateNoticeAfterInstall(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TOKENSAVER_ROOT", root)
	app := filepath.Join(root, "app")
	os.MkdirAll(app, 0o755)
	exe := installPath()
	os.WriteFile(exe, []byte("old"), 0o644)
	updStart, updNote, updChecked = nil, "", time.Time{}
	initUpdateNotice()
	if n := updateNotice(); n != "" {
		t.Fatalf("notice before any update: %q", n)
	}
	// a new program with the same version: nothing to load
	updChecked = time.Time{}
	os.WriteFile(exe, []byte("same version, rebuilt"), 0o644)
	os.Chtimes(exe, time.Now(), time.Now().Add(time.Hour))
	os.WriteFile(filepath.Join(app, "version.txt"), []byte(appVersion), 0o644)
	if n := updateNotice(); n != "" {
		t.Fatalf("notice for the same version: %q", n)
	}
	// a newer version installed while this process runs
	updChecked = time.Time{}
	os.WriteFile(exe, []byte("newer program"), 0o644)
	os.Chtimes(exe, time.Now(), time.Now().Add(2*time.Hour))
	os.WriteFile(filepath.Join(app, "version.txt"), []byte("99.0.0"), 0o644)
	n := updateNotice()
	if !strings.Contains(n, "99.0.0") || !strings.Contains(n, appVersion) || !strings.Contains(n, "restart_claude") {
		t.Fatalf("got %q", n)
	}
	if updateNotice() != n {
		t.Fatal("notice should stay the same once found")
	}
}