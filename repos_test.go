package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeT(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitT(t *testing.T, work, msg string) {
	t.Helper()
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "-q", "-m", msg)
	gitT(t, work, "push", "-q", "--tags", "origin", "HEAD:main")
}

// fakeGitHub makes <tmp>/gh/acme/tool.git (one commit, tag v1) and points TokenSaver's
// repo copies at it instead of github.com; it returns a work clone to push more from.
func fakeGitHub(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	oldHome, oldBase := appHome, repoURLBase
	t.Cleanup(func() { appHome, repoURLBase = oldHome, oldBase })
	appHome = filepath.Join(dir, "home")
	repoURLBase = filepath.ToSlash(filepath.Join(dir, "gh")) + "/"
	repoSweepMu.Lock()
	repoSweptAt = time.Now() // no background sweeps while testing
	repoSweepMu.Unlock()
	bare := filepath.Join(dir, "gh", "acme", "tool.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, bare, "init", "-q", "--bare")
	gitT(t, bare, "symbolic-ref", "HEAD", "refs/heads/main")
	work := filepath.Join(dir, "work")
	gitT(t, dir, "init", "-q", work)
	gitT(t, work, "remote", "add", "origin", bare)
	writeT(t, filepath.Join(work, "main.go"), "package main\n\nfunc main() {}\n")
	writeT(t, filepath.Join(work, "pkg", "util.go"), "package pkg\n\nfunc Old() {}\n")
	gitT(t, work, "add", "-A")
	gitT(t, work, "commit", "-q", "-m", "one")
	gitT(t, work, "tag", "v1")
	gitT(t, work, "push", "-q", "--tags", "origin", "HEAD:main")
	return work
}

func TestRepoPathResolvesToCopy(t *testing.T) {
	fakeGitHub(t)
	c, ok, err := repoResolve("acme/tool/pkg/util.go")
	if !ok || err != nil {
		t.Fatalf("resolve: ok=%v err=%v", ok, err)
	}
	if want := filepath.Join(appHome, "repos", "acme", "tool", "pkg", "util.go"); c.Local != want {
		t.Fatalf("local = %s, want %s", c.Local, want)
	}
	out, _, err := callTool("read", json.RawMessage(`{"path":"acme/tool/pkg/util.go","mode":"head","lines":5}`))
	if err != nil || !strings.Contains(out, "func Old") {
		t.Fatalf("read through owner/name: %v\n%s", err, out)
	}
	out, _, err = callTool("find", json.RawMessage(`{"path":["acme/tool"],"text":"func Old","recursive":true}`))
	if err != nil || !strings.Contains(out, "acme/tool/") || strings.Contains(out, appHome) {
		t.Fatalf("find should answer with owner/name paths: %v\n%s", err, out)
	}
	out, _, err = callTool("outline", json.RawMessage(`{"path":"acme/tool"}`))
	if err != nil || !strings.Contains(out, "Old") || !strings.Contains(out, "main") {
		t.Fatalf("outline of a repo: %v\n%s", err, out)
	}
	for _, p := range []string{`D:\x\y`, "C:/x/y", "/abs/x", "~/x", "%TEMP%/x", "justone", "a/../b"} {
		if _, ok := parseRepoSpec(p); ok {
			t.Errorf("%q should not be a repo path", p)
		}
	}
	if sp, ok := parseRepoSpec("acme/tool@feature/x/pkg"); !ok || !sp.HasRef || sp.Ref != "feature/x/pkg" {
		t.Errorf("ref parse: %+v", sp)
	}
}

func TestRepoSecondUseUpdatesAndCloneUpdates(t *testing.T) {
	work := fakeGitHub(t)
	if _, _, err := repoResolve("acme/tool"); err != nil {
		t.Fatal(err)
	}
	writeT(t, filepath.Join(work, "new.go"), "package main\n")
	commitT(t, work, "two")
	old := repoFresh
	repoFresh = 0
	t.Cleanup(func() { repoFresh = old })
	if _, _, err := repoResolve("acme/tool/new.go"); err != nil {
		t.Fatalf("second use did not update the copy: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "mine")
	args, _ := json.Marshal(map[string]string{"action": "clone", "url": "acme/tool", "to": dest})
	if _, err := toolGit(args); err != nil {
		t.Fatalf("first clone: %v", err)
	}
	writeT(t, filepath.Join(work, "third.go"), "package main\n")
	commitT(t, work, "three")
	out, err := toolGit(args)
	if err != nil || !strings.Contains(out, "updated") || !exists(filepath.Join(dest, "third.go")) {
		t.Fatalf("cloning into an existing clone should update it: %v\n%s", err, out)
	}
	other := filepath.Join(t.TempDir(), "other")
	writeT(t, filepath.Join(other, "x.txt"), "x")
	args, _ = json.Marshal(map[string]string{"action": "clone", "url": "acme/tool", "to": other})
	if _, err := toolGit(args); err == nil {
		t.Fatal("cloning into a folder holding something else should fail")
	}
}

func TestRepoRefAndCompare(t *testing.T) {
	work := fakeGitHub(t)
	writeT(t, filepath.Join(work, "pkg", "util.go"), "package pkg\n\nfunc New() {}\n\nfunc Extra() {}\n")
	writeT(t, filepath.Join(work, "added.go"), "package main\n")
	commitT(t, work, "two")
	c, _, err := repoResolve("acme/tool@v1/pkg/util.go")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(c.Local); !strings.Contains(string(b), "func Old") || c.Label != "acme/tool@v1" {
		t.Fatalf("@v1 should give the old file, got %q (%s)", b, c.Label)
	}
	cur, _, err := repoResolve("acme/tool/pkg/util.go")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(cur.Local); !strings.Contains(string(b), "func New") {
		t.Fatalf("main should give the new file, got %q", b)
	}
	args, _ := json.Marshal(map[string]any{"action": "compare", "path": "acme/tool@v1", "to": "acme/tool"})
	out, err := toolGit(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Only in B (1): added.go", "pkg/util.go +3 -1", "Same: 1 file"} {
		if !strings.Contains(out, want) {
			t.Errorf("compare lacks %q:\n%s", want, out)
		}
	}
}

func TestCompareFolders(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	writeT(t, filepath.Join(a, "same.go"), "x\ny\n")
	writeT(t, filepath.Join(b, "same.go"), "x\r\ny\r\n")
	writeT(t, filepath.Join(a, "changed.go"), "1\n2\n3\n4\n")
	writeT(t, filepath.Join(b, "changed.go"), "1\n2b\n3\n4\n5\n")
	writeT(t, filepath.Join(a, "onlya.go"), "a\n")
	writeT(t, filepath.Join(b, "onlyb.go"), "b\n")
	writeT(t, filepath.Join(a, "node_modules", "dep.js"), "x\n")
	writeT(t, filepath.Join(a, "sys_windows.go"), "a\n")
	writeT(t, filepath.Join(b, "sys_linux.go"), "a\nb\n")
	run := func(extra map[string]any) string {
		t.Helper()
		m := map[string]any{"action": "compare", "path": a, "to": b}
		for k, v := range extra {
			m[k] = v
		}
		args, _ := json.Marshal(m)
		out, err := toolGit(args)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run(nil)
	for _, want := range []string{"Only in A (2): onlya.go, sys_windows.go", "Only in B (2): onlyb.go, sys_linux.go", "changed.go +2 -1", "Same: 1 file"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "node_modules") {
		t.Errorf("node_modules should be skipped:\n%s", out)
	}
	out = run(map[string]any{"pair": true, "paths": []string{"changed.go"}, "full": true})
	for _, want := range []string{"sys_windows.go ↔ sys_linux.go +1 -0", "Only in A (1): onlya.go", "\n-2\n", "\n+2b\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("lacks %q:\n%s", want, out)
		}
	}
}

func TestRepoCopiesExpireAndAreReadOnly(t *testing.T) {
	fakeGitHub(t)
	if _, _, err := repoResolve("acme/tool@v1"); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"action": "commit", "path": "acme/tool", "message": "x"})
	if _, err := toolGit(args); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("commit on a temporary copy should be refused, got %v", err)
	}
	if got := repoSweepNow(time.Now().Add(-repoKeep)); len(got) != 0 {
		t.Fatalf("copies in use were removed: %v", got)
	}
	long := time.Now().Add(-2 * repoKeep)
	sp := repoSpec{Owner: "acme", Name: "tool"}
	for _, d := range []string{"tool", "tool@v1"} {
		if err := os.Chtimes(repoStateFile(sp, d+".used"), long, long); err != nil {
			t.Fatal(err)
		}
	}
	base := filepath.Join(appHome, "repos", "acme", "tool")
	if got := repoSweepNow(time.Now().Add(-repoKeep)); len(got) != 2 || exists(base) || exists(base+"@v1") {
		t.Fatalf("unused copies should be gone, removed %v", got)
	}
}
