package main

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// a Perplexity chat document as the accessibility tree gives it, in document order
type ayFix struct{ items []ayItem }

func (f *ayFix) add(kind string, x, y, w int, name string) *ayFix {
	f.items = append(f.items, ayItem{kind: kind, x: x, y: y, w: w, h: 44, name: name})
	return f
}
func (f *ayFix) query(y int, text ...string) *ayFix { // time, [Edit query], [Copy query], the bubble
	f.add("Text", 1500, y, 94, "5:44 PM").add("Button", 1613, y, 48, "Edit query").add("Button", 1669, y, 48, "Copy query")
	for i, t := range text {
		f.add("Text", 799, y+40+i*50, 1000, t)
	}
	return f
}
func (f *ayFix) answer(y int, lines ...string) *ayFix { // step header, paragraphs, action row
	f.add("Button", 542, y, 1306, "Finished 12s")
	for i, l := range lines {
		f.add("Text", 542, y+60+i*60, 1200, l)
	}
	end := y + 60 + len(lines)*60
	return f.add("Button", 526, end, 64, "Copy").add("Button", 598, end, 64, "Helpful").add("Button", 670, end, 64, "Not helpful").add("Button", 742, end, 64, "Fork")
}
func (f *ayFix) docs(sidebar ...string) []ayDoc {
	side := ayDoc{name: "Perplexity Sidebar"}
	for i, s := range sidebar {
		side.items = append(side.items, ayItem{kind: "Button", x: 20, y: 1150 + i*84, w: 386, h: 72, name: s})
	}
	chat := ayDoc{name: "Perplexity", items: append(append([]ayItem{}, f.items...),
		ayItem{kind: "Edit", x: 582, y: 1936, w: 1242, h: 56, name: "Ask a follow-up"}, ayItem{kind: "Button", x: 1760, y: 2004, w: 64, h: 64, name: "Submit"})}
	return []ayDoc{chat, side, {name: "Perplexity App Bar", items: []ayItem{{kind: "Button", x: 1642, w: 92, h: 86, name: "Minimize"}}}}
}

func ayPendingOf(t *testing.T, docs []ayDoc) (string, string) { return ayPendingIn(t, docs, false) }

// ayPendingIn: anyQ is project / full auto mode, where any closing question counts.
func ayPendingIn(t *testing.T, docs []ayDoc, anyQ bool) (string, string) {
	t.Helper()
	chat, _ := ayChatDoc(docs)
	if chat == nil {
		t.Fatal("no chat document found")
	}
	tu, ok := ayNewestAnswer(*chat)
	if !ok {
		return "", ""
	}
	q, _, key := ayAnswerQuestion(tu, anyQ)
	return q, key
}

const fullAutoPrompt = `FULL AUTO PROJECT MODE — READ THIS FIRST - You have my permission for everything in this task. The answer to every "should I…?", "can I…?" or "do you want me to…?" is YES.`

// The misfires logged on 14 Nov 2026 in data\logs\autoyes.log.
func TestAutoYesMisfires(t *testing.T) {
	// 17:32:55 answered yes to: " is YES.  (text from the user's own prompt)
	f := (&ayFix{}).query(100, fullAutoPrompt, "Don't ask me questions and don't wait for me to confirm anything.").
		answer(400, "Turning on project mode and writing step 1 now.", "Step 1 is on disk: index.html, game.js and the tests.")
	if q, _ := ayPendingIn(t, f.docs(), true); q != "" {
		t.Errorf(`17:32:55: answered the user's own prompt: %q`, q)
	}

	// 17:44:10 answered yes to: or docs. Just the game and the tests. ORDER OF WORK ...  (prompt text again)
	f = (&ayFix{}).query(100, "No README or docs. Just the game and the tests.",
		`ORDER OF WORK (finish and test each step before moving on) 1. Game loop + Pip's movement + level 1 → run the code checker, play-test it in the browser. 2. Enemies, items, "?`).
		answer(500, "Step 1 is written and the checker passed.")
	if q, _ := ayPendingIn(t, f.docs(), true); q != "" {
		t.Errorf(`17:44:10: answered text from the user's query bubble: %q`, q)
	}

	// 17:44:25 sidebar history glued onto the real question: the question alone must come out
	f = (&ayFix{}).query(100, "go on").
		answer(300, "The tests pass.", "Open PixelFox index.html in the TokenSaver Chrome browser for a smoke test?")
	docs := f.docs("create an android app, it should scan for wifi, ... do not ask permission before doing something, just go ahead until you are done")
	q, key := ayPendingIn(t, docs, true) // full auto mode was on
	if q != "Open PixelFox index.html in the TokenSaver Chrome browser for a smoke test?" || key == "" {
		t.Errorf("17:44:25: question = %q, want the browser question alone", q)
	}

	// 17:48:22 answered yes to: yes  (its own reply, shown as a query bubble)
	f = (&ayFix{}).query(100, "build it").answer(300, "Open PixelFox index.html in the TokenSaver Chrome browser for a smoke test?").
		query(700, "yes")
	if q, _ := ayPendingOf(t, f.docs()); q != "" {
		t.Errorf("17:48:22: reacted after its own yes: %q", q)
	}
	f.answer(900, "The game opened and Pip moves.") // the reply to that yes asks nothing
	if q, _ := ayPendingOf(t, f.docs()); q != "" {
		t.Errorf("17:48:22: answered a plain answer after its own yes: %q", q)
	}
	if !ayOwnReply(" Yes. ") || ayOwnReply("Shall I proceed?") {
		t.Error("its own typed reply must never count as a question")
	}
}

func TestAutoYesClosingQuestionOnly(t *testing.T) {
	// a question asked early in the answer is not what it waits on
	f := (&ayFix{}).query(100, "build it").answer(300, "Shall I proceed with the build? I went ahead anyway.", "The build passed.", "Nothing else to do.")
	if q, _ := ayPendingOf(t, f.docs()); q != "" {
		t.Errorf("answered a question that does not close the answer: %q", q)
	}
	// Perplexity's action card: log the card's question, not "Reply yes to proceed"
	f = (&ayFix{}).query(100, "test it")
	f.add("Button", 542, 250, 1306, "Finished 13s").add("Text", 542, 290, 1196, "Run a syntax check and smoke test (node) inside D:\\FightingGame on your PC?").
		add("Text", 560, 381, 56, "text").add("Button", 1726, 372, 48, "Copy code").add("Text", 560, 433, 2283, "cmd in D:\\FightingGame: node --check on the four JS files").
		add("Text", 542, 534, 96, "Reply ").add("Text", 638, 534, 52, "yes").add("Text", 690, 534, 219, " to proceed or ").add("Text", 909, 534, 41, "no").add("Text", 950, 534, 155, " to cancel.").
		add("Button", 526, 590, 64, "Copy").add("Button", 598, 590, 64, "Helpful")
	if q, _ := ayPendingOf(t, f.docs()); q != "Run a syntax check and smoke test (node) inside D:\\FightingGame on your PC?" {
		t.Errorf("action card: question = %q", q)
	}
	// still answering
	f = (&ayFix{}).query(100, "build it").answer(300, "Shall I proceed with the build?")
	f.add("Button", 1700, 1900, 64, "Stop response (Esc)")
	if q, _ := ayPendingOf(t, f.docs()); q != "" {
		t.Errorf("answered while Perplexity was still writing: %q", q)
	}
}

func TestAutoYesAnswersOnce(t *testing.T) {
	oh := appHome
	appHome = t.TempDir()
	t.Cleanup(func() { appHome = oh })
	st := &autoYesState{On: true}
	a := newAyWatcher(st)
	sent := 0
	a.send = func(string) error { sent++; return nil }
	a.landed = func(winInfo, string) bool { return true }
	w := winInfo{hwnd: 7}
	p := ayPending{question: "Open PixelFox index.html in the TokenSaver Chrome browser for a smoke test?", around: "Open PixelFox index.html ...?", key: "k1"}
	// 17:47:45 + 17:47:55: the same question read again must not get a second yes
	for i := 0; i < 5; i++ {
		a.answer(w, p, false)
	}
	if sent != 1 || st.Replies != 1 {
		t.Errorf("sent %d yes(es) to one question, want 1", sent)
	}
	// a reply that does not land is retried, then left alone; never more than 3 tries
	a.landed = func(winInfo, string) bool { return false }
	p2 := ayPending{question: "Commit the files?", around: "Commit the files?", key: "k2"}
	for i := 0; i < 8; i++ {
		a.answer(w, p2, true)
	}
	if sent != 4 {
		t.Errorf("tries for a reply that never landed: %d, want 3", sent-1)
	}
	// deleting is the user's call
	a.landed = func(winInfo, string) bool { return true }
	a.answer(w, ayPending{question: "Shall I proceed?", around: "This will delete the old build folder. Shall I proceed?", key: "k3"}, true)
	if sent != 4 {
		t.Error("answered a question about deleting")
	}
	a.send = func(string) error { return errors.New("no box") }
	if a.answer(w, ayPending{question: "Run it?", around: "Run it?", key: "k4"}, true) {
		t.Error("a failed send counted as answered")
	}
}

func TestAyWaitingThreads(t *testing.T) {
	side := &ayDoc{name: "Perplexity Sidebar", items: []ayItem{
		{kind: "Button", x: 20, y: 1158, w: 386, h: 72, name: "make PixelFox"},
		{kind: "Text", x: 60, y: 1190, w: 200, h: 30, name: "Waiting for your response"},
		{kind: "Button", x: 20, y: 1242, w: 386, h: 72, name: "build the fighting game"},
	}}
	got := ayWaitingThreads(side)
	if len(got) != 1 || got[0].name != "make PixelFox" {
		t.Errorf("waiting threads = %+v, want the PixelFox entry", got)
	}
	if ayWaitingThreads(nil) != nil {
		t.Error("no sidebar, no waiting threads")
	}
}

func TestParseAyDump(t *testing.T) {
	out := "D\tPerplexity\r\nI\tButton\t1669\t694\t48\t48\t0\tCopy query\nI\tText\t542\t154\t1254\t96\t1\tShall I go on?\nD\tPerplexity Sidebar\nI\tButton\t20\t1158\t386\t72\t0\thistory item\n"
	docs, err := parseAyDump(out)
	if err != nil || len(docs) != 2 || len(docs[0].items) != 2 || !docs[0].items[1].off || docs[1].items[0].name != "history item" {
		t.Fatalf("parse: %+v %v", docs, err)
	}
	if _, err := parseAyDump("E access denied\n"); err == nil {
		t.Error("a UI Automation error must come back as an error")
	}
}

func TestCDPKeys(t *testing.T) {
	for in, want := range map[string]cdpKey{
		"ArrowRight": {"ArrowRight", "ArrowRight", 39, ""}, "right": {"ArrowRight", "ArrowRight", 39, ""},
		"Space": {" ", "Space", 32, " "}, "a": {"a", "KeyA", 65, "a"}, "7": {"7", "Digit7", 55, "7"}, "F5": {"F5", "F5", 116, ""},
	} {
		if got, err := parseCDPKey(in); err != nil || got != want {
			t.Errorf("%s: %+v %v, want %+v", in, got, err, want)
		}
	}
	ks, err := parseCDPCombo("ctrl+a")
	if err != nil || len(ks) != 2 || comboMods(ks) != 2 {
		t.Errorf("ctrl+a: %+v %v", ks, err)
	}
	if _, err := parseCDPKey("nosuchkey"); err == nil {
		t.Error("an unknown key name must fail")
	}
}

func TestConsoleEvents(t *testing.T) {
	e, nav, ok := parseConsoleEvent([]byte(`{"method":"Runtime.exceptionThrown","params":{"exceptionDetails":{"text":"Uncaught","url":"file:///D:/Perplexity%20Apps/PixelFox/game.js","lineNumber":41,"exception":{"description":"TypeError: pip is undefined\n    at loop (game.js:42)"}}}}`))
	if !ok || nav || e.level != "uncaught" || !strings.Contains(e.text, "TypeError: pip is undefined") || e.where != "game.js:42" {
		t.Errorf("exception: %+v", e)
	}
	e, _, ok = parseConsoleEvent([]byte(`{"method":"Runtime.consoleAPICalled","params":{"type":"warning","args":[{"type":"string","value":"slow frame"},{"type":"number","value":42}]}}`))
	if !ok || e.level != "warn" || e.text != "slow frame 42" {
		t.Errorf("console.warn: %+v", e)
	}
	if _, nav, _ := parseConsoleEvent([]byte(`{"method":"Page.frameNavigated","params":{"frame":{"id":"1"}}}`)); !nav {
		t.Error("a main-frame load must reset the console list")
	}
	if _, nav, _ := parseConsoleEvent([]byte(`{"method":"Page.frameNavigated","params":{"frame":{"id":"2","parentId":"1"}}}`)); nav {
		t.Error("an iframe load must not reset the console list")
	}
}

func TestFixURLPaths(t *testing.T) {
	if got := fixURL(`D:\Perplexity Apps\PixelFox\index.html`); got != "file:///D:/Perplexity Apps/PixelFox/index.html" {
		t.Errorf("windows path: %s", got)
	}
	if got := fixURL("file:///D:/Perplexity Apps/PixelFox/index.html"); got != "file:///D:/Perplexity Apps/PixelFox/index.html" {
		t.Errorf("file URL: %s", got)
	}
	if got := fixURL("/home/me/game/index.html"); got != "file:///home/me/game/index.html" {
		t.Errorf("linux path: %s", got)
	}
}

// TestBrowserPlaysCanvas drives a real headless Chrome on CI (never on a PC, where TokenSaver's
// own browser holds the port): a canvas game must change after keys and its errors must show.
func TestBrowserPlaysCanvas(t *testing.T) {
	if os.Getenv("CI") != "true" || browserExe() == "" {
		t.Skip("needs CI with Chrome")
	}
	if _, err := cdpTargets(); err == nil {
		t.Skip("a browser already holds the DevTools port")
	}
	oh := appHome
	appHome = t.TempDir()
	t.Cleanup(func() {
		toolBrowser(jsonRaw(map[string]any{"action": "close"}))
		time.Sleep(500 * time.Millisecond)
		appHome = oh
	})
	page := t.TempDir() + "/game.html"
	html := `<!doctype html><canvas id=c width=400 height=200></canvas><script>
const g = document.getElementById('c').getContext('2d'); let x = 20;
addEventListener('keydown', e => { if (e.key === 'ArrowRight') x += 5; if (e.code === 'Space') console.error('jump pressed'); });
function draw() { g.fillStyle = '#000'; g.fillRect(0, 0, 400, 200); g.fillStyle = '#f80'; g.fillRect(x, 80, 40, 40); g.fillStyle = '#fff'; g.font = '28px sans-serif'; g.fillText('SCORE ' + x, 200, 40); requestAnimationFrame(draw); }
draw(); console.log('game ready'); setTimeout(() => { undefinedThing.go(); }, 10);
</script>`
	if err := os.WriteFile(page, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := toolBrowser(jsonRaw(map[string]any{"action": "open", "url": page})); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	s, imgs, err := toolBrowserFull(jsonRaw(map[string]any{"action": "screenshot", "changes": true}))
	if err != nil || len(imgs) != 1 || len(imgs[0].data) < 500 {
		t.Fatalf("screenshot: %s %v", s, err)
	}
	s, imgs, err = toolBrowserFull(jsonRaw(map[string]any{"action": "do", "then": "screenshot", "changes": true,
		"steps": []map[string]any{{"hold": "ArrowRight", "secs": 1}, {"press": "Space"}, {"wait": 0.3}}}))
	if err != nil || len(imgs) != 1 || !strings.Contains(s, "the picture changed") || strings.Contains(s, "failed") {
		t.Errorf("after input: %s %v", s, err)
	}
	s, _, err = toolBrowserFull(jsonRaw(map[string]any{"action": "console"}))
	if err != nil || !strings.Contains(s, "jump pressed") || !strings.Contains(s, "undefinedThing") || !strings.Contains(s, "game ready") {
		t.Errorf("console: %s %v", s, err)
	}
}
