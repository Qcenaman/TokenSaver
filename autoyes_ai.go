package main

// Auto-yes with judgement: when Perplexity finishes a message, the local AI (Qwen "coder" on the
// ELAI hub) reads it and decides whether Perplexity is waiting for the user and what to reply: a
// plain go-ahead gets "yes", a choice gets the option to take, and in project / full auto mode a
// question the user left to it gets a short "decide yourself" answer. Deleting or overwriting
// still needs project / full auto mode. When the local AI can't run (not set up, GPU taken by
// ComfyUI or training, error), the simple phrase rules take over as before.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ayVerdict is the local AI's reading of Perplexity's newest message.
type ayVerdict struct {
	Waiting bool   `json:"waiting"` // Perplexity can't go on without the user
	Kind    string `json:"kind"`    // go_ahead | choice | question | none
	Risky   bool   `json:"risky"`   // deleting, overwriting, formatting, uninstalling, force-pushing, outside the project
	Reply   string `json:"reply"`   // what to type
	Reason  string `json:"reason"`  // one line, for the log
}

const ayJudgeSystem = `You read the newest message of an AI assistant (Perplexity) that is doing a task for the user on the user's PC, and decide whether it is now waiting for the user before it can go on, and what the user should reply. Reply with one JSON object only, no other text:
{"waiting": true|false, "kind": "go_ahead"|"choice"|"question"|"none", "risky": true|false, "reply": "...", "reason": "..."}
kind:
- go_ahead: it asks for permission or confirmation to continue or to run something ("Reply yes to proceed", "Shall I…?", "Do you want me to…?", "Confirm…"). reply: "yes", or the exact word it asked for.
- choice: it asks the user to pick between options. reply: the option to take (the one it recommends, else the most sensible), written the way it asked for (number, letter or name).
- question: it needs information or a decision that only the user can give. reply: see the mode.
- none: it is not waiting (a status update, a finished report, still working, or a question that is only rhetorical). reply: "".
risky: true when going ahead would delete, overwrite or replace files, format or wipe, uninstall, reset or force-push, change system settings, spend money, or touch something outside the task's folder.
reason: at most 15 words.`

func ayJudgeUser(lines []string, query, mode string) string {
	msg := strings.Join(lines, "\n")
	if len(msg) > 6000 { // the end holds the question
		msg = "…" + msg[len(msg)-6000:]
	}
	var b strings.Builder
	b.WriteString("Mode: ")
	switch mode {
	case "full":
		b.WriteString("FULL AUTO. The user told the assistant to do everything without asking and to decide for itself. For a question, reply with a short instruction to decide itself and continue, e.g. \"You decide: pick the best option, note it in the report, and continue.\" Only wiping a drive still needs the user.")
	case "project":
		b.WriteString("PROJECT MODE for one folder. Work inside that folder is pre-approved. For a question, reply with a short instruction to decide itself and continue.")
	default:
		b.WriteString("NORMAL. Only plain go-aheads and choices are answered for the user. For a question, reply \"\".")
	}
	if q := strings.TrimSpace(query); q != "" {
		b.WriteString("\n\nThe user's last message to the assistant (for context): " + clip(q, 1500))
	}
	b.WriteString("\n\nThe assistant's newest message:\n<<<\n" + msg + "\n>>>")
	return b.String()
}

var ayJSONRe = regexp.MustCompile(`(?s)\{.*\}`)

// ayParseVerdict reads the local AI's JSON answer (it may wrap it in text or a code block).
func ayParseVerdict(out string) (ayVerdict, error) {
	var v ayVerdict
	m := ayJSONRe.FindString(out)
	if m == "" {
		return v, fmt.Errorf("no JSON in the local AI's answer: %s", clip(out, 120))
	}
	if err := json.Unmarshal([]byte(m), &v); err != nil {
		return v, fmt.Errorf("unreadable JSON from the local AI: %v", err)
	}
	v.Kind = strings.ToLower(strings.TrimSpace(v.Kind))
	switch v.Kind {
	case "go_ahead", "choice", "question", "none":
	default:
		return v, fmt.Errorf("unknown kind %q from the local AI", v.Kind)
	}
	if v.Kind == "none" {
		v.Waiting = false
	}
	v.Reply = ayCleanReply(v.Reply)
	if v.Kind == "go_ahead" && v.Reply == "" {
		v.Reply = "yes"
	}
	v.Reason = clip(strings.Join(strings.Fields(v.Reason), " "), 160)
	return v, nil
}

// ayCleanReply: one line, no surrounding quotes, short.
func ayCleanReply(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, "\"'“”`")
	return clip(strings.TrimSpace(s), 400)
}

// ayMode is how much auto-yes may decide: full, project or normal.
func ayMode() string {
	switch p := activeProject(); {
	case p == fullMode:
		return "full"
	case p != "":
		return "project"
	}
	return "normal"
}

// ayJudgeLocal asks the local AI about one message. It refuses (and the phrase rules take over)
// when no local AI is set up or the GPU is held by ComfyUI / training and the model isn't loaded.
func ayJudgeLocal(lines []string, query, mode string) (ayVerdict, error) {
	if cfg.AIScript == "" && cfg.AIEndpoint == "" {
		return ayVerdict{}, errors.New("no local AI is set up (ai_script in config.json)")
	}
	if rem, _ := gpuGate("auto-yes", "", aiWouldLoad()); rem != "" {
		return ayVerdict{}, errors.New("the GPU is in use and the local AI isn't loaded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	j := &Job{ID: fmt.Sprintf("autoyes-%d", time.Now().UnixNano()), ctx: ctx, cancel: cancel}
	c, release, err := openAI(j, cfg.AIModel)
	if err != nil {
		return ayVerdict{}, err
	}
	defer release()
	out, err := c.chatImg(ayJudgeSystem, ayJudgeUser(lines, query, mode), nil, 300)
	if err != nil {
		return ayVerdict{}, err
	}
	return ayParseVerdict(out)
}

func ayHash(lines []string) string {
	s := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(s[:8])
}

// judge handles one finished message with the local AI; ok false means the AI couldn't decide and
// the phrase rules should handle it instead.
func (a *ayWatcher) judge(w winInfo, p ayPending, stable bool) (ok bool) {
	k := p.ansKey
	if _, done := a.answered[k]; done || a.judged[k] {
		return true
	}
	if !stable && a.seen[w.hwnd] != k { // a message still being written changes between reads
		a.seen[w.hwnd] = k
		return true
	}
	if !a.aiDown.IsZero() && time.Since(a.aiDown) < 2*time.Minute {
		return false // it just failed: don't wait on it again for every message
	}
	mode := ayMode()
	v, err := a.judgeFn(p.lines, p.query, mode)
	if err != nil {
		if a.aiDown.IsZero() || time.Since(a.aiDown) > 30*time.Minute {
			autoYesLogf("local AI not available (%s); using the simple phrase rules", clip(err.Error(), 200))
		}
		a.aiDown = time.Now()
		return false
	}
	a.aiDown = time.Time{}
	a.judged[k] = true
	last := ""
	if n := len(p.lines); n > 0 {
		last = clip(p.lines[n-1], 200)
	}
	if !v.Waiting || v.Kind == "none" {
		return true
	}
	tail := strings.Join(p.lines[max(0, len(p.lines)-6):], "\n")
	if v.Risky || ayRisky.MatchString(tail) || ayAlwaysRisky.MatchString(tail) {
		if !projectAllows(tail, activeProject()) {
			a.answered[k] = time.Now()
			autoYesLogf("left for you (local AI: %s, %s): %s", v.Kind, v.Reason, last)
			return true
		}
	}
	if v.Kind == "question" && (mode == "normal" || v.Reply == "") {
		a.answered[k] = time.Now()
		autoYesLogf("left for you (local AI: a question only you can answer, %s): %s", v.Reason, last)
		return true
	}
	if v.Reply == "" || (ayOwnReply(v.Reply) && v.Kind != "go_ahead") {
		a.answered[k] = time.Now()
		autoYesLogf("left for you (local AI gave no usable reply): %s", last)
		return true
	}
	if err := a.send(fmt.Sprint(w.hwnd), v.Reply); err != nil {
		a.answered[k] = time.Now()
		autoYesLogf("could not answer (%v): %s", err, last)
		return true
	}
	if !a.landed(w, k) {
		if a.fails[k]++; a.fails[k] < 3 {
			delete(a.judged, k)
			autoYesLogf("typed %q but it did not reach Perplexity, will retry: %s", v.Reply, last)
			return true
		}
		a.answered[k] = time.Now()
		autoYesLogf("gave up after 3 tries, left for you: %s", last)
		return true
	}
	a.answered[k] = time.Now()
	a.st.Replies++
	autoYesLogf("replied %q (local AI: %s, %s) to: %s", v.Reply, v.Kind, v.Reason, last)
	return true
}
