//go:build !windows

package main

// The Linux helper starts with systemd and is shared with Claude, so local mode
// neither starts nor stops it.
func startHelper() string { return "" }

func stopHelper() string { return "" }

func helperInstalled() bool { return true }

func helperHowTo() string {
	return "The admin helper isn't set up, so commands that need admin won't work. To set it up once, run: tokensaver install-helper"
}
