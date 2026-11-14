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
		// Perplexity's action card: a step title glued to "Commit the new files ... ?" over the tool call
		regexp.MustCompile(`(Create|Commit|Run|Write|Edit|Update|Apply|Execute|Build|Push|Start|Make|Add|Install|Delete|Remove|Move|Rename|Test)\b[^?\n]{5,300}\?`),
	}
	// a go-ahead for these is the user's to give
	ayRisky = regexp.MustCompile(`(?i)\b(delet|remov|eras|wip(e|ing)|format|uninstall|overwrit|drop\b|reset\b|force[- ]?push|purge|destroy)`)
)

var (
	// any sentence of the newest message that mentions yes ("type yes", "just say yes", ...)
	ayYes = regexp.MustCompile(`(?i)[^.!?\n\[\]]{0,160}\byes\b[^.!?\n\[\]]{0,160}[.!?]?`)
	// project mode: any question that closes the message, however it is worded
	ayAnyQuestion = regexp.MustCompile(`[^?\n\[\]]{5,300}\?`)
)

// confirmQuestion reads a Perplexity window's text (UI Automation, screen order) and returns
// the confirmation question its newest message is waiting on, the text around it, and a key
// for that message. All empty when Perplexity is still answering, when the user has written
// since, or when the newest message asks nothing of the kind. In project mode any closing
// question counts.
func confirmQuestion(text string) (question, around, key string) {
	return confirmQuestionIn(text, activeProject() != "")
}

func confirmQuestionIn(text string, anyQuestion bool) (question, around, key string) {
	if strings.Contains(text, "[Stop response") {
		return "", "", ""
	}
	end := max(strings.LastIndex(text, "[Helpful]"), strings.LastIndex(text, "[Fork]")) // closes the newest answer
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
	pats := append([]*regexp.Regexp{}, ayAsk...)
	if anyQuestion {
		pats = append(pats, ayAnyQuestion)
	}
	for _, re := range pats {
		if locs := re.FindAllStringIndex(tail, -1); len(locs) > 0 {
			if l := locs[len(locs)-1]; l[0] > at {
				at, question = l[0], strings.TrimSpace(tail[l[0]:l[1]])
			}
		}
	}
	if question == "" { // a yes asked for in the closing part of the message
		if m := ayYes.FindAllString(tail, -1); len(m) > 0 {
			question = strings.TrimSpace(m[len(m)-1])
		}
	}
	if question == "" || ayOwnReply(question) {
		return "", "", ""
	}
	sum := sha256.Sum256([]byte(msg))
	return question, tail, hex.EncodeToString(sum[:8])
}

// autoYesSend clicks Perplexity's "Ask a follow-up" box in window win (a handle), types yes like a
// person (its editor ignores a value set through UI Automation) and presses Enter. A step that
// fails comes back as an error, so a reply that never landed is not logged as answered.
func autoYesSend(win string) error {
	do := func(steps []map[string]any) error {
		out, err := toolWindow(jsonRaw(map[string]any{"action": "do", "window": win, "steps": steps}))
		if err == nil && strings.Contains(out, "failed:") {
			err = fmt.Errorf("%s", strings.TrimSpace(out))
		}
		return err
	}
	// ctrl+a first, so yes replaces whatever the box holds instead of landing inside it
	typed := []map[string]any{{"wait": 0.3}, {"press": "ctrl+a"}, {"type": "yes"}, {"wait": 0.3}, {"press": "enter"}}
	err := do(append([]map[string]any{{"click": "Ask a follow-up"}}, typed...))
	if err != nil { // no box by that name: click the text on screen instead
		err = do(append([]map[string]any{{"click_text": "Ask a follow-up"}}, typed...))
	}
	return err
}

// ayPending is the go-ahead one window is waiting on.
type ayPending struct {
	question, around, key string
}

// ayRead returns what window w waits on (key empty: nothing), plus sidebar threads marked as waiting.
func ayRead(w winInfo) (p ayPending, waiting []ayItem, err error) {
	anyQ := activeProject() != ""
	if !isWin { // no accessibility documents here: the window's text, the old way
		text, err := toolWindow(jsonRaw(map[string]any{"action": "text", "window": fmt.Sprint(w.hwnd), "max_chars": 400000}))
		if err != nil {
			return p, nil, err
		}
		p.question, p.around, p.key = confirmQuestionIn(text, anyQ)
		return p, nil, nil
	}
	docs, err := ayReadWindow(w)
	if err != nil {
		return p, nil, err
	}
	chat, side := ayChatDoc(docs)
	waiting = ayWaitingThreads(side)
	if chat == nil {
		return p, waiting, nil
	}
	if t, ok := ayNewestAnswer(*chat); ok {
		p.question, p.around, p.key = ayAnswerQuestion(t, anyQ)
	}
	return p, waiting, nil
}

// autoYesLanded tells whether the message answered (key) is no longer waiting: the reply was
// sent, so Perplexity is answering or a newer query stands after it.
func autoYesLanded(w winInfo, key string) bool {
	for i := 0; i < 4; i++ {
		time.Sleep(time.Second)
		p, _, err := ayRead(w)
		if err != nil {
			continue
		}
		if p.key != key {
			return true
		}
	}
	return false
}

// ayWatcher keeps what auto-yes has answered, so each question gets one yes only.
type ayWatcher struct {
	st       *autoYesState
	answered map[string]time.Time // key → when answered; never answered twice
	seen     map[uintptr]string   // window → key read last round (a finished message reads the same twice)
	fails    map[string]int
	send     func(win string) error            // autoYesSend; tests swap it
	landed   func(w winInfo, key string) bool // autoYesLanded; tests swap it
}

func newAyWatcher(st *autoYesState) *ayWatcher {
	return &ayWatcher{st: st, answered: map[string]time.Time{}, seen: map[uintptr]string{}, fails: map[string]int{},
		send: autoYesSend, landed: autoYesLanded}
}

// answer handles one pending go-ahead in window w; true when a yes went out and landed.
func (a *ayWatcher) answer(w winInfo, p ayPending, stable bool) bool {
	if p.key == "" {
		delete(a.seen, w.hwnd)
		return false
	}
	if _, done := a.answered[p.key]; done {
		return false
	}
	if !stable && a.seen[w.hwnd] != p.key { // still being written, maybe: wait for the next round
		a.seen[w.hwnd] = p.key
		return false
	}
	if ayRisky.MatchString(p.around) || ayAlwaysRisky.MatchString(p.around) {
		if !projectAllows(p.around, activeProject()) {
			a.answered[p.key] = time.Now()
			autoYesLogf("left for you (it is about deleting or overwriting, or names something outside the project): %s", p.question)
			return false
		}
		autoYesLogf("project mode: delete/overwrite inside %s", activeProject())
	}
	if err := a.send(fmt.Sprint(w.hwnd)); err != nil {
		a.answered[p.key] = time.Now()
		autoYesLogf("could not answer (%v): %s", err, p.question)
		return false
	}
	if !a.landed(w, p.key) {
		if a.fails[p.key]++; a.fails[p.key] < 3 {
			autoYesLogf("typed yes but it did not reach Perplexity, will retry: %s", p.question)
			return false
		}
		a.answered[p.key] = time.Now()
		autoYesLogf("gave up after 3 tries, left for you: %s", p.question)
		return false
	}
	a.answered[p.key] = time.Now()
	a.st.Replies++
	autoYesLogf("answered yes to: %s", p.question)
	return true
}

// visitWaiting opens a background chat the sidebar marks as waiting, answers it if it asks for a
// go-ahead, and goes back to the chat that was showing.
func (a *ayWatcher) visitWaiting(w winInfo, th ayItem) {
	cx, cy := th.x+th.w/2, th.y+th.h/2
	if _, err := toolWindow(jsonRaw(map[string]any{"action": "do", "window": fmt.Sprint(w.hwnd),
		"steps": []map[string]any{{"click_at": []int{cx, cy}, "screen": true}, {"wait": 2.5}}})); err != nil {
		return
	}
	autoYesLogf("opened a waiting chat in the background: %s", clip(th.name, 80))
	if p, _, err := ayRead(w); err == nil {
		a.answer(w, p, true)
	}
	toolWindow(jsonRaw(map[string]any{"action": "do", "window": fmt.Sprint(w.hwnd), "steps": []map[string]any{{"click": "Back"}, {"wait": 1}}}))
}

// autoYesRun is the watcher (`tokensaver autoyes run`); it runs in the user's session and
// watches every Perplexity window.
func autoYesRun() int {
	if len(ayWindows()) == 0 {
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
	a := newAyWatcher(&st)
	// what is already on screen when it switches on was asked before: not answered
	for _, w := range ayWindows() {
		if p, _, err := ayRead(w); err == nil && p.key != "" {
			a.answered[p.key] = time.Now()
		}
	}
	visited := map[string]time.Time{}
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
		wins := ayWindows()
		if len(wins) == 0 {
			if missing++; missing >= 3 {
				return stop("the Perplexity window closed")
			}
			continue
		}
		missing = 0
		for _, w := range wins {
			p, waiting, err := ayRead(w)
			if err != nil {
				continue
			}
			a.answer(w, p, false)
			for _, th := range waiting { // a second chat waiting behind the one on screen
				if p.key != "" && a.answered[p.key].IsZero() {
					break // this window's own question first
				}
				if time.Since(visited[th.name]) < 30*time.Second {
					continue
				}
				visited[th.name] = time.Now()
				a.visitWaiting(w, th)
			}
		}
		for k, t := range a.answered { // forget after the longest auto-yes run
			if time.Since(t) > 4*time.Hour {
				delete(a.answered, k)
			}
		}
	}
}

func autoYesOn() string {
	if loadAutoYes().alive() {
		return autoYesStatus()
	}
	exe, _ := os.Executable()
	if err := exec.Command(exe, "autoyes", "run").Start(); err != nil {
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
