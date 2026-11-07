//go:build linux

package main

import (
	"testing"
)

func TestServeSingleInstance(t *testing.T) {
	appHome = t.TempDir()
	rel, err := serveSingleInstance()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := serveSingleInstance(); err == nil {
		t.Fatal("second serve got the lock")
	}
	rel()
	rel2, err := serveSingleInstance()
	if err != nil {
		t.Fatal("lock not released:", err)
	}
	rel2()
}

func TestHelperClientOK(t *testing.T) {
	cfg := helperConfig{UID: 1000, Client: "/home/u/Claude Apps/TokenSaver/app/tokensaver", Root: "/home/u/Claude Apps/TokenSaver"}
	for c, want := range map[string]bool{
		cfg.Client:                true,
		cfg.Client + " (deleted)": true,
		cfg.Client + ".old":       true,
		"/home/u/Claude Apps/TokenSaver/old/TokenSaver program/tokensaver.old.20261102": true,
		"/tmp/tokensaver":                         false,
		"/home/u/evil/tokensaver.old":             false,
		"/home/u/Claude Apps/TokenSaver/app/bash": false,
	} {
		if helperClientOK(c, cfg) != want {
			t.Errorf("helperClientOK(%q) = %v", c, !want)
		}
	}
}

func TestSystemdQuote(t *testing.T) {
	if got := systemdQuote("/home/u/Claude Apps/TokenSaver/app/tokensaver"); got != `"/home/u/Claude Apps/TokenSaver/app/tokensaver"` {
		t.Fatal(got)
	}
	if got := systemdQuote("/usr/bin/x"); got != "/usr/bin/x" {
		t.Fatal(got)
	}
}
