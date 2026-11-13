package main

// Project mode: the user picks one folder that a chat app (Perplexity and the like) may work in
// without asking first. While it is on, the server instructions tell the assistant to go ahead
// with edits, deletes, commands, builds, tests and commits inside that folder, and auto-yes runs
// too and also answers delete or overwrite questions as long as they name nothing outside it.
// Off by default; switches itself off after eight hours.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const projectMax = 8 * time.Hour

// fullMode is the project "folder" of full mode: anywhere on the PC, any question answered,
// except wiping or formatting a whole drive.
const fullMode = "*"

type projectState struct {
	Folder string    `json:"folder,omitempty"`
	Since  time.Time `json:"since,omitempty"`
}

func projectPath() string { return filepath.Join(appHome, "project.json") }

// activeProject is the project-mode folder, or "" when project mode is off or has run out.
func activeProject() string {
	var st projectState
	b, err := os.ReadFile(projectPath())
	if err != nil || json.Unmarshal(b, &st) != nil || st.Folder == "" {
		return ""
	}
	if time.Since(st.Since) > projectMax {
		return ""
	}
	return st.Folder
}

func projectSince() time.Time {
	var st projectState
	if b, err := os.ReadFile(projectPath()); err == nil {
		json.Unmarshal(b, &st)
	}
	return st.Since
}

// projectNote is what the assistant is told while project mode is on ("" when off).
func projectNote() string {
	f := activeProject()
	if f == "" {
		return ""
	}
	if f == fullMode {
		return "Full auto mode is on (the user switched it on and pre-approved everything). Work anywhere on this PC without asking the user first: " +
			"files in any folder, commands, admin commands (set confirmed:true), installs, settings, services, builds, commits, pushes and force-pushes. " +
			"Do not stop to ask \"shall I proceed?\" or wait for a yes; carry on to the end and then report what you did. " +
			"Only ask before wiping or formatting a whole drive."
	}
	return "Project mode is on for " + f + " (the user switched it on). Inside that folder, work without asking the user first: " +
		"create, edit and delete files, run commands, builds and tests, commit and push. Do not stop to ask \"shall I proceed?\" " +
		"or wait for a yes; carry on to the end and then report what you did. Still ask before touching anything outside that folder, " +
		"before force-pushing or rewriting git history, and before changing the PC itself (installing, settings, services, admin commands)."
}

// inProject tells whether path p lies inside the project folder.
func inProject(p, folder string) bool {
	q, err := expandPath(p)
	if err != nil {
		return false
	}
	q = filepath.Clean(q)
	folder = filepath.Clean(folder)
	if isWin {
		q, folder = strings.ToLower(q), strings.ToLower(folder)
	}
	return q == folder || strings.HasPrefix(q, folder+string(filepath.Separator))
}

var (
	// absolute paths an assistant writes in a question
	pathInTextRe = regexp.MustCompile(`(?i)(\b[a-z]:[\\/][^\s"'<>|?*` + "`" + `]*|~[\\/][^\s"'<>|?*` + "`" + `]*|(^|\s)/(home|users|etc|var|usr|opt|mnt)/[^\s"'<>|?*` + "`" + `]*)`)
	// wiping a whole drive: never answered, not even in full mode
	ayDriveWipe = regexp.MustCompile(`(?i)\b(format\s+[a-z]:|format (the |a |this )?(drive|disk|partition)|diskpart|clean (all|disk)|wipe (the |a |this )?(drive|disk|partition)|initialize-disk|clear-disk)`)
	// never answered by auto-yes in project mode
	ayAlwaysRisky = regexp.MustCompile(`(?i)\b(format|uninstall|drop\b|reset --hard|force[- ]?push|push --force|purge|destroy|wip(e|ing)|registry|regedit)`)
)

// projectAllows tells whether a delete/overwrite question may be answered yes in project mode:
// every path it names is inside the folder, and it is nothing auto-yes always leaves to the user.
func projectAllows(text, folder string) bool {
	if folder == fullMode {
		return !ayDriveWipe.MatchString(text)
	}
	if folder == "" || ayAlwaysRisky.MatchString(text) {
		return false
	}
	for _, m := range pathInTextRe.FindAllString(text, -1) {
		m = strings.TrimRight(strings.TrimSpace(m), ".,;:)]}")
		if !inProject(m, folder) {
			return false
		}
	}
	return true
}

func projectOn(folder string) string {
	if folder == fullMode {
		return modeOn(fullMode, "full auto mode on")
	}
	p, err := expandPath(folder)
	if err != nil {
		return "project mode: " + err.Error()
	}
	fi, err := os.Stat(p)
	if err != nil || !fi.IsDir() {
		return "project mode: no such folder: " + p
	}
	if v := filepath.VolumeName(p); filepath.Clean(p) == v+string(filepath.Separator) || filepath.Clean(p) == filepath.Clean(userHome()) {
		return "project mode: pick a project folder, not a whole drive or your home folder: " + p
	}
	return modeOn(filepath.Clean(p), "project mode on for "+p)
}

func modeOn(folder, logLine string) string {
	if err := writeJSONAtomic(projectPath(), projectState{Folder: folder, Since: time.Now()}); err != nil {
		return "project mode could not be saved: " + err.Error()
	}
	autoYesLogf("%s", logLine)
	autoYesOff() // restart so it picks up the project-mode limit
	ay := autoYesOn()
	return projectNote() + "\n" + ay + "\nA chat that was already open may need a new chat to read these instructions; until then, follow this message."
}

func projectOff() string {
	if f := activeProject(); f != "" {
		autoYesLogf("project mode off for %s", f)
	}
	os.Remove(projectPath())
	return "project mode: off. " + autoYesOff()
}

func projectStatus() string {
	f := activeProject()
	if f == "" {
		return "project mode: off. " + autoYesStatus()
	}
	if f == fullMode {
		return fmt.Sprintf("full auto mode: on since %s (everything answered yes except wiping a drive), switches itself off at %s. %s",
			projectSince().Format("15:04"), projectSince().Add(projectMax).Format("15:04"), autoYesStatus())
	}
	return fmt.Sprintf("project mode: on for %s since %s, switches itself off at %s. %s",
		f, projectSince().Format("15:04"), projectSince().Add(projectMax).Format("15:04"), autoYesStatus())
}

// projectSwitch takes "off", "status", "" (status), "all" (full mode) or a folder to switch on for.
func projectSwitch(arg string) string {
	a := strings.TrimSpace(arg)
	switch strings.ToLower(a) {
	case "off", "stop":
		return projectOff()
	case "", "status":
		return projectStatus()
	case "all", "full", "everything", "*":
		return projectOn(fullMode)
	}
	if strings.HasPrefix(strings.ToLower(a), "on ") {
		a = strings.TrimSpace(a[3:])
	}
	return projectOn(a)
}

// projectCLI is `tokensaver project <folder>|on <folder>|off|status`.
func projectCLI(args []string) int {
	fmt.Println(projectSwitch(strings.Join(args, " ")))
	return 0
}

// projectTool handles window action:project (text = folder to switch on for, off, or status);
// handled is false for every other action.
func projectTool(raw json.RawMessage) (out string, handled bool) {
	var a struct {
		Action string `json:"action"`
		Text   string `json:"text"`
	}
	if json.Unmarshal(raw, &a) != nil || a.Action != "project" {
		return "", false
	}
	return projectSwitch(a.Text), true
}
