package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectAllows(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "app")
	os.MkdirAll(proj, 0o755)
	inside := filepath.Join(proj, "build")
	outside := filepath.Join(dir, "other")
	cases := []struct {
		text string
		want bool
	}{
		{"This will delete the old build folder. Shall I proceed?", true},
		{"I'll remove " + inside + " and rebuild. Shall I proceed?", true},
		{"I'll remove " + outside + " too. Shall I proceed?", false},
		{`I need to delete C:\Windows\Temp\x. Shall I proceed?`, false},
		{"Shall I force-push to main?", false},
		{"Should I uninstall the old SDK first?", false},
		{"Can I run git reset --hard to clean up?", false},
	}
	for _, c := range cases {
		if got := projectAllows(c.text, proj); got != c.want {
			t.Errorf("projectAllows(%q) = %v, want %v", c.text, got, c.want)
		}
	}
	if projectAllows("This will delete the old build folder. Shall I proceed?", "") {
		t.Error("allowed with project mode off")
	}
}

func TestInProject(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "app")
	if !inProject(filepath.Join(proj, "src", "a.go"), proj) || !inProject(proj, proj) {
		t.Error("path inside the project not recognised")
	}
	if inProject(proj+"2", proj) || inProject(dir, proj) {
		t.Error("path outside the project counted as inside")
	}
}
