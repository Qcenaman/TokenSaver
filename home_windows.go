package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// profileHome finds the user's profile folder for the LocalSystem service: the one recorded
// when the service was installed, else the profile that was used last.
func profileHome() string {
	if a := loadLinkCfg().AndroidUserHome; a != "" {
		if h := filepath.Dir(a); exists(filepath.Join(h, "NTUSER.DAT")) {
			return h
		}
	}
	users := filepath.Join(os.Getenv("SystemDrive")+`\`, "Users")
	ents, _ := os.ReadDir(users)
	best, bestT := "", time.Time{}
	for _, e := range ents {
		switch strings.ToLower(e.Name()) {
		case "public", "default", "default user", "all users", "defaultuser0":
			continue
		}
		if fi, err := os.Stat(filepath.Join(users, e.Name(), "NTUSER.DAT")); err == nil && fi.ModTime().After(bestT) {
			best, bestT = filepath.Join(users, e.Name()), fi.ModTime()
		}
	}
	return best
}
