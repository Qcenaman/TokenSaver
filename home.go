package main

import (
	"os"
	"regexp"
	"strings"
	"sync"
)

// userHome is the signed-in user's home folder. As the boot service TokenSaver runs as
// LocalSystem, whose own profile is not where the user's files are, so the user's real
// folder is looked up instead. TOKENSAVER_USER_HOME overrides it.
func userHome() string {
	if v := os.Getenv("TOKENSAVER_USER_HOME"); v != "" {
		return v
	}
	return detectedHome()
}

var detectedHome = sync.OnceValue(func() string {
	h, _ := os.UserHomeDir()
	if isSystemProfile(h) {
		if p := profileHome(); p != "" {
			return p
		}
	}
	return h
})

func isSystemProfile(p string) bool {
	return isWin && strings.Contains(strings.ToLower(p), "systemprofile")
}

// homeTokenRe: the ways chat clients write "my home folder" at the start of a path:
// ~, $HOME, ${HOME}, $env:USERPROFILE, %USERPROFILE%, %HOME%.
var homeTokenRe = regexp.MustCompile(`(?i)^(~|\$\{?(env:)?(HOME|USERPROFILE)\}?|%(HOME|USERPROFILE)%)($|[/\\])`)

// expandHome replaces a leading home token with the user's home folder.
func expandHome(p string) string {
	m := homeTokenRe.FindStringSubmatchIndex(p)
	if m == nil {
		return p
	}
	h := userHome()
	if h == "" {
		return p
	}
	return h + p[m[3]:]
}

// absExample is the example shown when a path isn't absolute.
func absExample() string {
	if isWin {
		return `D:\Projects\app`
	}
	return "/home/user/Projects/app"
}
