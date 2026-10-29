package main

import "testing"

func TestCkIgnored(t *testing.T) {
	ign := []string{"data/", "tokensaver.exe"}
	cases := map[string]bool{"data/x/y.js": true, "data/": true, "tokensaver.exe": true, "dat/x.js": false, "check.go": false}
	for p, want := range cases {
		if got := ckIgnored(p, ign); got != want {
			t.Errorf("ckIgnored(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestCkGoLine(t *testing.T) {
	m := ckGoLine.FindStringSubmatch(`.\check.go:12:3: this value of err is never used (SA4006)`)
	if m == nil || m[1] != `.\check.go` || m[2] != "12" || m[5] != "SA4006" {
		t.Fatalf("staticcheck line: %q", m)
	}
	m = ckGoLine.FindStringSubmatch(`vet: mcp.go:5:2: unreachable code`)
	if m == nil || m[1] != "mcp.go" || m[5] != "" {
		t.Fatalf("vet line: %q", m)
	}
}
