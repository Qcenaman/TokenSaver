//go:build linux

package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrashRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "share"))
	f := filepath.Join(dir, "docs", "a b%.txt")
	os.MkdirAll(filepath.Dir(f), 0o755)
	os.WriteFile(f, []byte("hello"), 0o644)
	if !useRecycleBin(f) {
		t.Fatal("trash not usable on the same disk")
	}
	at := time.Now()
	if err := recycleBatch([]string{f}); err != nil {
		t.Fatal(err)
	}
	if exists(f) {
		t.Fatal("file still there after trashing")
	}
	infos, _ := filepath.Glob(filepath.Join(homeTrash(), "info", "*.trashinfo"))
	if len(infos) != 1 {
		t.Fatalf("want 1 trashinfo, got %d", len(infos))
	}
	b, _ := os.ReadFile(infos[0])
	if !strings.Contains(string(b), "Path=") || !strings.Contains(string(b), "a%20b%25.txt") {
		t.Fatalf("bad trashinfo: %s", b)
	}
	if err := restoreRecycled(f, at); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(f); string(got) != "hello" {
		t.Fatalf("restored content %q", got)
	}
}

func TestListenerPid(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if pid := listenerPid(port); pid != os.Getpid() {
		t.Fatalf("listenerPid(%d) = %d, want %d", port, pid, os.Getpid())
	}
	ls, err := listening()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ls {
		found = found || p.port == port && p.addr == "127.0.0.1"
	}
	if !found {
		t.Fatal("port missing from listening()")
	}
}

func TestBuildCommandShells(t *testing.T) {
	for _, sh := range []string{"", "cmd", "bash"} {
		c, err := buildCommand(sh, `echo "${BASH_VERSION:+bash}"`)
		if err != nil {
			t.Fatal(err)
		}
		out, err := c.Output()
		if err != nil || strings.TrimSpace(string(out)) != "bash" {
			t.Fatalf("shell %q: %q %v", sh, out, err)
		}
	}
	if _, err := buildCommand("fish-or-so", "true"); err == nil {
		t.Fatal("unknown shell accepted")
	}
}

func TestProcsFullNames(t *testing.T) {
	ps, err := procs()
	if err != nil || len(ps) == 0 {
		t.Fatal(err)
	}
	self := false
	for _, p := range ps {
		self = self || p.pid == os.Getpid() && p.name != "" && p.mem > 0
	}
	if !self {
		t.Fatal("own process not listed with name and memory")
	}
}

func TestLinuxOCR(t *testing.T) {
	if !ocrAvailable() {
		t.Skip("tesseract not installed")
	}
	if _, err := exec.LookPath("convert"); err != nil {
		t.Skip("ImageMagick not installed")
	}
	appHome = t.TempDir()
	img := filepath.Join(appHome, "t.png")
	if out, err := exec.Command("convert", "-size", "900x200", "xc:white", "-fill", "black", "-pointsize", "64",
		"-annotate", "+40+120", "TokenSaver Linux", img).CombinedOutput(); err != nil {
		t.Skipf("convert can't draw text here: %s", out)
	}
	var pg ocrPage
	if _, err := runLinuxOCR(context.Background(), ocrReq{mode: "image", path: img, boxes: true}, func(p ocrPage) { pg = p }); err != nil {
		t.Fatal(err)
	}
	if pg.W != 900 || !strings.Contains(strings.ToLower(pg.text()), "tokensaver") {
		t.Fatalf("OCR got %dx%d %q", pg.W, pg.H, pg.text())
	}
	if len(pg.Lines) == 0 || len(pg.Lines[0].Words) == 0 {
		t.Fatal("no word boxes")
	}
}

func TestSofficeConvert(t *testing.T) {
	if sofficeBin() == "" {
		t.Skip("LibreOffice not installed")
	}
	appHome = t.TempDir()
	src := filepath.Join(appHome, "note.txt")
	os.WriteFile(src, []byte("TokenSaver on Linux\n"), 0o644)
	dst := filepath.Join(appHome, "out", "note.pdf")
	if err := sofficeConvert(src, dst); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(dst)
	if !strings.HasPrefix(string(b), "%PDF") {
		t.Fatal("not a PDF")
	}
}

func TestOtherGPUInfoNoPanic(t *testing.T) { _ = otherGPUInfo() }
