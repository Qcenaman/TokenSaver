package main

import "testing"

func TestNeedsUserSession(t *testing.T) {
	yes := []string{"git status", "git -C \"D:\\x\" push", "cd D:\\x && git pull", "gh pr list", "echo a; GIT.EXE fetch"}
	no := []string{"dir", "schtasks /Run", "copy D:\\git\\a b", "echo git", "python digit.py"}
	for _, c := range yes {
		if !needsUserSession(c) {
			t.Errorf("expected user session for %q", c)
		}
	}
	for _, c := range no {
		if needsUserSession(c) {
			t.Errorf("did not expect user session for %q", c)
		}
	}
}