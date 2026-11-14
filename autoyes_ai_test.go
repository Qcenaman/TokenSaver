package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAyParseVerdict(t *testing.T) {
	v, err := ayParseVerdict("Here you go:\n```json\n{\"waiting\": true, \"kind\": \"go_ahead\", \"risky\": false, \"reply\": \"\", \"reason\": \"asks to run the smoke test\"}\n```")
	if err != nil || !v.Waiting || v.Kind != "go_ahead" || v.Reply != "yes" {
		t.Fatalf("go-ahead: %+v %v", v, err)
	}
	if v, _ := ayParseVerdict(`{"waiting": true, "kind": "none", "reply": "x"}`); v.Waiting {
		t.Error("kind none is never waiting")
	}
	if v, _ := ayParseVerdict(`{"waiting": true, "kind": "choice", "reply": "\"Option 2\"\n"}`); v.Reply != "Option 2" {
		t.Errorf("reply not cleaned: %q", v.Reply)
	}
	if _, err := ayParseVerdict(`{"kind": "maybe"}`); err == nil {
		t.Error("an unknown kind must be refused")
	}
	if _, err := ayParseVerdict("I think it wants a yes"); err == nil {
		t.Error("no JSON must be an error, so the phrase rules take over")
	}
	u := ayJudgeUser([]string{"Step 1 done.", "Shall I build step 2?"}, "build the game", "full")
	if !strings.Contains(u, "FULL AUTO") || !strings.Contains(u, "Shall I build step 2?") || !strings.Contains(u, "build the game") {
		t.Errorf("prompt misses the mode, message or context:\n%s", u)
	}
}

// a watcher with a fake local AI and a fake Perplexity window
func ayTestWatcher(t *testing.T, v ayVerdict, err error) (*ayWatcher, *[]string, *int) {
	t.Helper()
	old := appHome
	appHome = t.TempDir()
	t.Cleanup(func() { appHome = old })
	a := newAyWatcher(&autoYesState{On: true})
	sent, calls := &[]string{}, new(int)
	a.send = func(_, text string) error { *sent = append(*sent, text); return nil }
	a.landed = func(winInfo, string) bool { return true }
	a.judgeFn = func([]string, string, string) (ayVerdict, error) { *calls++; return v, err }
	return a, sent, calls
}

func ayMsg(lines ...string) ayPending {
	return ayPending{lines: lines, ansKey: ayHash(lines)}
}

func TestAyJudgeReplies(t *testing.T) {
	w := winInfo{hwnd: 1}
	a, sent, calls := ayTestWatcher(t, ayVerdict{Waiting: true, Kind: "go_ahead", Reply: "yes", Reason: "asks to proceed"}, nil)
	m := ayMsg("Tests pass.", "Want me to open it in the browser for a smoke test?")
	a.handle(w, m, true)
	a.handle(w, m, true) // read again: judged and answered once
	if strings.Join(*sent, "|") != "yes" || *calls != 1 {
		t.Errorf("go-ahead: sent %v, local AI asked %d times", *sent, *calls)
	}

	a, sent, _ = ayTestWatcher(t, ayVerdict{Waiting: true, Kind: "choice", Reply: "2", Reason: "pick a renderer"}, nil)
	a.handle(w, ayMsg("Which renderer? 1) canvas 2) WebGL (recommended)"), true)
	if strings.Join(*sent, "|") != "2" {
		t.Errorf("choice: sent %v, want the option", *sent)
	}

	q := ayVerdict{Waiting: true, Kind: "question", Reply: "You decide: pick the best option, note it, and continue.", Reason: "asks for the art style"}
	a, sent, _ = ayTestWatcher(t, q, nil)
	a.handle(w, ayMsg("What art style do you want for the characters?"), true)
	if len(*sent) != 0 {
		t.Errorf("normal mode: a real question is the user's, sent %v", *sent)
	}
	a, sent, _ = ayTestWatcher(t, q, nil)
	writeJSONAtomic(projectPath(), projectState{Folder: fullMode, Since: time.Now()})
	a.handle(w, ayMsg("What art style do you want for the characters?"), true)
	if len(*sent) != 1 || !strings.HasPrefix((*sent)[0], "You decide") {
		t.Errorf("full auto mode: the question gets the local AI's reply, sent %v", *sent)
	}

	a, sent, _ = ayTestWatcher(t, ayVerdict{Waiting: true, Kind: "go_ahead", Risky: true, Reply: "yes", Reason: "deletes the build folder"}, nil)
	a.handle(w, ayMsg("This removes D:\\Games\\old-build entirely. Proceed?"), true)
	if len(*sent) != 0 {
		t.Errorf("risky go-ahead outside project / full auto mode must wait for the user, sent %v", *sent)
	}

	a, sent, _ = ayTestWatcher(t, ayVerdict{Waiting: false, Kind: "none", Reason: "final report"}, nil)
	a.handle(w, ayMsg("Done. Everything is pushed and released."), true)
	if len(*sent) != 0 {
		t.Errorf("a finished report needs no reply, sent %v", *sent)
	}
}

// the local AI is down: the phrase rules answer, and it isn't asked again for every message
func TestAyJudgeFallsBack(t *testing.T) {
	w := winInfo{hwnd: 1}
	a, sent, calls := ayTestWatcher(t, ayVerdict{}, errors.New("the GPU is in use and the local AI isn't loaded"))
	m := ayMsg("Reply yes to proceed or no to cancel.")
	m.key, m.question, m.around = "k1", "Reply yes to proceed or no to cancel.", "Reply yes to proceed or no to cancel."
	a.handle(w, m, true)
	if strings.Join(*sent, "|") != "yes" {
		t.Errorf("fallback to the phrase rules: sent %v", *sent)
	}
	m2 := ayMsg("Shall I commit?")
	m2.key, m2.question, m2.around = "k2", "Shall I commit?", "Shall I commit?"
	a.handle(w, m2, true)
	if *calls != 1 {
		t.Errorf("after a failure the local AI is left alone for a while: asked %d times", *calls)
	}
}
