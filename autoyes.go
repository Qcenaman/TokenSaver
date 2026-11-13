package main

// Auto-yes: while switched on, answers "yes" in the Perplexity window when its newest message
// only asks to confirm. Off by default; switches itself off after two hours or when the
// Perplexity window closes; never answers a question about deleting or overwriting; every
// reply is logged.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	autoYesMax    = 2 * time.Hour
	autoYesWindow = "Perplexity"
)

type autoYesState struct {
	On      bool      `json:"on"`
	PID     int       `json:"pid,omitempty"`
	Since   time.Time `json:"since,omitempty"`
	Beat    time.Time `json:"beat,omitempty"`
	Replies int       `json:"replies"`
	Why     string    `json:"stopped_because,omitempty"`
}

func autoYesPath() string { return filepath.Join(appHome, "autoyes.json") }
func autoYesLog() string  { return filepath.Join(appHome, "logs", "autoyes.log") }

func loadAutoYes() autoYesState {
	var st autoYesState
	if b, err := os.ReadFile(autoYesPath()); err == nil {
		json.Unmarshal(b, &st)
	}
	return st
}

// autoYesLimit is how long auto-yes stays on: two hours, or until project mode ends.
func autoYesLimit() time.Duration {
	if activeProject() != "" {
		if d := projectSince().Add(projectMax).Sub(loadAutoYes().Since); d > autoYesMax {
			return d
		}
	}
	return autoYesMax
}

func (st autoYesState) alive() bool { return st.On && time.Since(st.Beat) < 20*time.Second }

func autoYesLogf(format string, a ...any) {
	os.MkdirAll(filepath.Dir(autoYesLog()), 0o755)
	f, err := os.OpenFile(autoYesLog(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

var (
	// ways an assistant asks for a go-ahead
	ayAsk = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(type|reply|say|respond|answer|send)\b[^.?!\n]{0,30}["'“‘]?\byes\b[^.?!\n]{0,60}`),
		regexp.MustCompile(`(?i)\b(shall|should|may|can) i (proceed|continue|go ahead|start|run)\b[^?\n]{0,120}\?`),
		regexp.MustCompile(`(?i)\b(do|would) you (want|like) me to\b[^?\n]{0,160}\?`),
		regexp.MustCompile(`(?i)\b(please )?confirm\b[^?\n]{0,160}\?`),
		regexp.MustCompile(`(?i)\bconfirm (to|and i|so i)\b[^.\n]{0,80}`),
	}
	// a go-ahead for these is the user's to give
	ayRisky = regexp.MustCompile(`(?i)\b(delet|remov|eras|wip(e|ing)|format|uninstall|overwrit|drop\b|reset\b|force[- ]?push|purge|destroy)`)
)

// confirmQuestion reads a Perplexity window's text (UI Automation, screen order) and returns
// the confirmation question its newest message is waiting on, the text around it, and a key
// for that message. All empty when Perplexity is still answering, when the user has written
// since, or when the newest message asks nothing of the kind.
func confirmQuestion(text string) (question, around, key string) {
	if strings.Contains(text, "[Stop response") {
		return "", "", ""
	}
	end := strings.LastIndex(text, "[Helpful]") // closes the newest answer
	if end < 0 || strings.Contains(text[end:], "[Edit query]") {
		return "", "", ""
	}
	start := strings.LastIndex(text[:end], "[Copy query]") // the question it answers
	if start < 0 {
		start = 0
	}
	msg := text[start:end]
	tail := msg
	if len(tail) > 900 { // the go-ahead question closes the message
		tail = strings.ToValidUTF8(tail[len(tail)-900:], "")
	}
	at := -1
	for _, re := range ayAsk {
		if locs := re.FindAllStringIndex(tail, -1); len(locs) > 0 {
			if l := locs[len(locs)-1]; l[0] > at {
				at, question = l[0], strings.TrimSpace(tail[l[0]:l[1]])
			}
		}
	}
	if question == "" {
		return "", "", ""
	}
	sum := sha256.Sum256([]byte(msg))
	return question, tail, hex.EncodeToString(sum[:8])
}

func autoYesWindowText() (string, error) {
	return toolWindow(jsonRaw(map[string]any{"action": "text", "window": autoYesWindow, "max_chars": 400000}))
}

func autoYesSend() error {
	steps := func(into string) []map[string]any {
		typ := map[string]any{"type": "yes"}
		if into != "" {
			typ["into"] = into
		}
		return []map[string]any{typ, {"press": "enter"}}
	}
	_, err := toolWindow(jsonRaw(map[string]any{"action": "do", "window": autoYesWindow, "steps": steps("Ask")}))
	if err != nil { // no box by that name: type into the window as it is
		_, err = toolWindow(jsonRaw(map[string]any{"action": "do", "window": autoYesWindow, "steps": steps("")}))
	}
	return err
}

// autoYesRun is the watcher (`tokensaver autoyes run`); it runs in the user's session.
func autoYesRun() int {
	if _, err := autoYesWindowText(); err != nil {
		writeJSONAtomic(autoYesPath(), autoYesState{Why: "the Perplexity window is not open"})
		return 1
	}
	st := autoYesState{On: true, PID: os.Getpid(), Since: time.Now(), Beat: time.Now()}
	writeJSONAtomic(autoYesPath(), st)
	autoYesLogf("switched on")
	stop := func(why string) int {
		st.On, st.Why = false, why
		writeJSONAtomic(autoYesPath(), st)
		autoYesLogf("switched off: %s (%d repl(ies))", why, st.Replies)
		return 0
	}
	var answered, seen string
	missing := 0
	for {
		time.Sleep(3 * time.Second)
		if cur := loadAutoYes(); !cur.On || cur.PID != st.PID {
			autoYesLogf("switched off (%d repl(ies))", st.Replies)
			return 0
		}
		if time.Since(st.Since) > autoYesLimit() {
			return stop("its time limit passed")
		}
		st.Beat = time.Now()
		writeJSONAtomic(autoYesPath(), st)
		text, err := autoYesWindowText()
		if err != nil {
			if missing++; missing >= 3 {
				return stop("the Perplexity window closed")
			}
			continue
		}
		missing = 0
		q, around, key := confirmQuestion(text)
		if key == "" || key == answered {
			seen = ""
			continue
		}
		if key != seen { // the same finished message twice in a row, so it is not still being written
			seen = key
			continue
		}
		answered = key
		if ayRisky.MatchString(around) || ayAlwaysRisky.MatchString(around) {
			if !projectAllows(around, activeProject()) {
				autoYesLogf("left for you (it is about deleting or overwriting, or names something outside the project): %s", q)
				continue
			}
			autoYesLogf("project mode: delete/overwrite inside %s", activeProject())
		}
		if err := autoYesSend(); err != nil {
			autoYesLogf("could not answer (%v): %s", err, q)
			continue
		}
		st.Replies++
		autoYesLogf("answered yes to: %s", q)
	}
}

func autoYesOn() string {
	if loadAutoYes().alive() {
		return autoYesStatus()
	}
	exe, _ := os.Executable()
	var err error
	if inServiceSession() {
		_, err = runInUserSession(exe, []string{"autoyes", "run"}, "", 0)
	} else {
		err = exec.Command(exe, "autoyes", "run").Start()
	}
	if err != nil {
		return "auto-yes could not start: " + err.Error()
	}
	for i := 0; i < 10 && !loadAutoYes().alive(); i++ {
		time.Sleep(500 * time.Millisecond)
	}
	return autoYesStatus()
}

func autoYesOff() string {
	st := loadAutoYes()
	st.On, st.Why = false, "switched off"
	writeJSONAtomic(autoYesPath(), st)
	return autoYesStatus()
}

func autoYesStatus() string {
	st := loadAutoYes()
	if st.alive() {
		return fmt.Sprintf("auto-yes: on since %s, %d repl(ies), switches itself off at %s or when the Perplexity window closes. Log: %s",
			st.Since.Format("15:04"), st.Replies, st.Since.Add(autoYesLimit()).Format("15:04"), autoYesLog())
	}
	s := "auto-yes: off"
	if st.Why != "" {
		s += " (" + st.Why + ")"
	}
	return s + ". Log: " + autoYesLog()
}

func autoYesSwitch(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "on", "start":
		return autoYesOn()
	case "off", "stop":
		return autoYesOff()
	}
	return autoYesStatus()
}

// autoYesCLI is `tokensaver autoyes on|off|status` (run is the watcher itself).
func autoYesCLI(args []string) int {
	mode := ""
	if len(args) > 0 {
		mode = args[0]
	}
	if mode == "run" {
		return autoYesRun()
	}
	fmt.Println(autoYesSwitch(mode))
	return 0
}

// autoYesTool handles window action:auto_yes (text on|off|status); handled is false for every other action.
func autoYesTool(raw json.RawMessage) (out string, handled bool) {
	var a struct {
		Action string `json:"action"`
		Text   string `json:"text"`
	}
	if json.Unmarshal(raw, &a) != nil || a.Action != "auto_yes" {
		return "", false
	}
	return autoYesSwitch(a.Text), true
}
