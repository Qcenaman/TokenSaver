package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestChatFileName(t *testing.T) {
	cases := map[string]string{
		`What is a/b? "quotes" <x>|y*`: "What is a b quotes x y",
		"  trailing dots...  ":         "trailing dots",
		"CON":                          "_CON",
		"nul.txt":                      "_nul.txt",
		"":                             "Untitled",
		"tab\tand\nnewline":            "tab and newline",
		"C:\\path\\name":               "C path name",
	}
	for in, want := range cases {
		if got := chatFileName(in, 80); got != want {
			t.Errorf("chatFileName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("ä", 200)
	if got := chatFileName(long, 80); len([]rune(got)) != 80 {
		t.Errorf("long name not cut to 80 runes: %d", len([]rune(got)))
	}
}

func TestChatRelPath(t *testing.T) {
	m := chatMeta{ID: "1a2b3c4d-5e6f-7a8b-9c0d-112233445566", Title: "Fix: the build?", Created: "2026-10-31T10:00:00Z", Project: "Token/Saver"}
	got := chatRelPath(m)
	if !strings.HasPrefix(got, filepath.Join("Token Saver", "2026-10-3")) || !strings.HasSuffix(got, " Fix the build [1a2b3c4d].md") {
		t.Fatalf("path %q", got)
	}
	m.Project, m.Title = "", ""
	if got := chatRelPath(m); !strings.HasPrefix(got, "No project"+string(filepath.Separator)) || !strings.Contains(got, "Untitled [1a2b3c4d]") {
		t.Fatalf("no-project path %q", got)
	}
}

func TestPlanChatSync(t *testing.T) {
	root := t.TempDir()
	mk := func(id, title, upd string) chatMeta {
		return chatMeta{ID: id, Title: title, Created: "2026-10-01T00:00:00Z", Updated: upd}
	}
	same, changed, renamed, missing := mk("aaaaaaaa-1", "same", "u1"), mk("bbbbbbbb-1", "changed", "u2"), mk("cccccccc-1", "new title", "u1"), mk("dddddddd-1", "gone file", "u1")
	idx := chatIndex{Chats: map[string]chatIndexEntry{}}
	for _, m := range []chatMeta{same, changed, missing} {
		idx.Chats[m.ID] = chatIndexEntry{Path: chatRelPath(m), Updated: "u1"}
	}
	idx.Chats[renamed.ID] = chatIndexEntry{Path: chatRelPath(mk("cccccccc-1", "old title", "u1")), Updated: "u1"}
	for _, m := range []chatMeta{same, changed} {
		p := filepath.Join(root, chatRelPath(m))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	brandNew := mk("eeeeeeee-1", "brand new", "u1")
	remote := []chatMeta{same, changed, renamed, missing, brandNew}

	todo, unchanged := planChatSync(idx, remote, root, false)
	got := map[string]chatTodo{}
	for _, t := range todo {
		got[t.Meta.ID] = t
	}
	if unchanged != 1 || len(todo) != 4 {
		t.Fatalf("unchanged %d, todo %d (%v)", unchanged, len(todo), todo)
	}
	if _, ok := got[same.ID]; ok {
		t.Error("unchanged chat must not be re-fetched")
	}
	if !got[brandNew.ID].New || got[changed.ID].New {
		t.Error("new/updated flags wrong")
	}
	if got[renamed.ID].OldPath == "" || got[renamed.ID].OldPath == got[renamed.ID].Path {
		t.Error("renamed chat must carry its old path")
	}
	if _, ok := got[missing.ID]; !ok {
		t.Error("chat whose file is missing must be re-fetched")
	}
	if todo, unchanged := planChatSync(idx, remote, root, true); len(todo) != 5 || unchanged != 0 {
		t.Errorf("full must re-fetch all: %d / %d", len(todo), unchanged)
	}
}

func TestRenderChatMarkdown(t *testing.T) {
	raw := `{"uuid":"c1","name":"Trip plan","model":"claude-x","created_at":"2026-10-01T00:00:00Z","updated_at":"2026-10-02T00:00:00Z",
	 "current_leaf_message_uuid":"m4",
	 "chat_messages":[
	  {"uuid":"m1","sender":"human","index":0,"parent_message_uuid":"00000000-0000-4000-8000-000000000000","text":"Plan a trip","attachments":[{"file_name":"map.pdf"}],"files":[{"file_name":"photo.png"}]},
	  {"uuid":"m2","sender":"assistant","index":1,"parent_message_uuid":"m1","content":[{"type":"text","text":"OLD BRANCH"}]},
	  {"uuid":"m3","sender":"assistant","index":2,"parent_message_uuid":"m1","content":[
	     {"type":"thinking","thinking":"secret"},
	     {"type":"tool_use","name":"web_search","input":{"query":"best beaches\nPalawan"}},
	     {"type":"tool_result","name":"web_search","content":[{"type":"text","text":"RESULT BODY"}]},
	     {"type":"text","text":"Go to El Nido."}]},
	  {"uuid":"m4","sender":"human","index":3,"parent_message_uuid":"m3","text":"Thanks"}
	 ]}`
	var cv ccConv
	if err := json.Unmarshal([]byte(raw), &cv); err != nil {
		t.Fatal(err)
	}
	md := renderChatMarkdown(chatMeta{ID: "c1", Project: "Travel"}, cv)
	for _, want := range []string{
		"# Trip plan\n", "- URL: https://claude.ai/chat/c1", "- Created: 2026-10-01T00:00:00Z", "- Updated: 2026-10-02T00:00:00Z",
		"- Project: Travel", "- Model: claude-x", "## You\n\nPlan a trip", "Attachments: map.pdf, photo.png",
		"## Claude\n\n> Tool: web_search — best beaches Palawan\n\nGo to El Nido.", "## You\n\nThanks",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	for _, bad := range []string{"OLD BRANCH", "secret", "RESULT BODY"} {
		if strings.Contains(md, bad) {
			t.Errorf("markdown must not contain %q", bad)
		}
	}
	if strings.Index(md, "Plan a trip") > strings.Index(md, "El Nido") || strings.Index(md, "El Nido") > strings.Index(md, "Thanks") {
		t.Error("turns out of order")
	}
	if md2 := renderChatMarkdown(chatMeta{ID: "c1"}, cv); !strings.Contains(md2, "- Project: No project") {
		t.Error("missing project must read No project")
	}
}

func TestVisibleBranchWithoutLeaf(t *testing.T) {
	cv := ccConv{Messages: []ccMsg{{UUID: "b", Index: 1, Text: "2"}, {UUID: "a", Index: 0, Text: "1"}}}
	got := visibleBranch(cv)
	if len(got) != 2 || got[0].UUID != "a" {
		t.Fatalf("fallback must order by index: %+v", got)
	}
}

func TestChatIndexRoundTrip(t *testing.T) {
	root := t.TempDir()
	idx := loadChatIndex(root)
	idx.Chats["x"] = chatIndexEntry{Path: "No project/a.md", Updated: "u"}
	if err := saveChatIndex(root, idx); err != nil {
		t.Fatal(err)
	}
	back := loadChatIndex(root)
	if back.Chats["x"].Updated != "u" || back.Version != 1 {
		t.Fatalf("index round trip: %+v", back)
	}
}

func TestIsChallenge(t *testing.T) {
	if !isChallenge("claude.ai Just a moment...") || isChallenge("claude.ai Claude") {
		t.Fatal("challenge detection")
	}
}
