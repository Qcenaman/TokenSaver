package main

// chats: the user's claude.ai conversations saved to disk as Markdown, kept up to date.
// Everything goes through TokenSaver's own signed-in browser: fetch() runs inside a claude.ai
// tab, so the session cookie never leaves the browser. Chat text goes straight to files on
// disk; the tool result carries only counts and paths.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const chatsNeedSignIn = "needs sign-in: open the browser with show:true (browser action open url claude.ai show:true), sign in, then sync again"

var errChatsNeedSignIn = errors.New(chatsNeedSignIn)

// chatsRoot is where the Markdown files live (D:\Claude Chats on Windows).
func chatsRoot() string {
	if v := os.Getenv("TOKENSAVER_CHATS_DIR"); v != "" {
		return v
	}
	if isWin {
		return `D:\Claude Chats`
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Claude Chats")
}

func chatsIndexPath(root string) string { return filepath.Join(root, "index.json") }

// --- index ---------------------------------------------------------------------------------

type chatIndexEntry struct {
	Path    string `json:"path"` // relative to the root
	Updated string `json:"updated_at"`
	Title   string `json:"title,omitempty"`
	Project string `json:"project,omitempty"`
}

type chatIndex struct {
	Version  int                       `json:"version"`
	LastSync time.Time                 `json:"last_sync,omitempty"`
	Chats    map[string]chatIndexEntry `json:"chats"`
}

func loadChatIndex(root string) chatIndex {
	idx := chatIndex{Version: 1, Chats: map[string]chatIndexEntry{}}
	if b, err := os.ReadFile(chatsIndexPath(root)); err == nil {
		json.Unmarshal(b, &idx)
	}
	if idx.Chats == nil {
		idx.Chats = map[string]chatIndexEntry{}
	}
	return idx
}

// chatMeta is one conversation as the list endpoints describe it.
type chatMeta struct {
	ID      string
	Org     string
	Title   string
	Created string
	Updated string
	Project string // project name, "" = no project
	Model   string
}

type chatTodo struct {
	Meta    chatMeta
	Path    string // relative path to write
	OldPath string // relative path it had before (title or project renamed), or ""
	New     bool
}

// planChatSync decides which chats to fetch: new ones, ones whose updated_at changed, ones
// whose file went missing or must move (title/project renamed), or all of them with full.
func planChatSync(idx chatIndex, remote []chatMeta, root string, full bool) (todo []chatTodo, unchanged int) {
	for _, m := range remote {
		rel := chatRelPath(m)
		e, known := idx.Chats[m.ID]
		switch {
		case !known:
			todo = append(todo, chatTodo{Meta: m, Path: rel, New: true})
		case full || e.Updated != m.Updated || e.Path != rel || !exists(filepath.Join(root, e.Path)):
			t := chatTodo{Meta: m, Path: rel}
			if e.Path != rel {
				t.OldPath = e.Path
			}
			todo = append(todo, t)
		default:
			unchanged++
		}
	}
	return todo, unchanged
}

// --- names ---------------------------------------------------------------------------------

var (
	badNameChars = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]+`)
	spaceRun     = regexp.MustCompile(`\s+`)
	reservedName = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[0-9]|lpt[0-9])(\..*)?$`)
)

// chatFileName makes s safe as a Windows file or folder name of at most max characters.
func chatFileName(s string, max int) string {
	s = badNameChars.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaceRun.ReplaceAllString(s, " "))
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:max]))
	}
	s = strings.TrimRight(s, ". ")
	if s == "" {
		s = "Untitled"
	}
	if reservedName.MatchString(s) {
		s = "_" + s
	}
	return s
}

func shortID(id string) string {
	id = strings.ReplaceAll(id, "-", "")
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func chatDate(ts string) string {
	if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
		return t.Local().Format("2006-01-02")
	}
	if len(ts) >= 10 {
		return ts[:10]
	}
	return "0000-00-00"
}

// chatRelPath is <project or "No project">\<yyyy-mm-dd> <title> [<short id>].md
func chatRelPath(m chatMeta) string {
	proj := "No project"
	if m.Project != "" {
		proj = chatFileName(m.Project, 60)
	}
	title := m.Title
	if strings.TrimSpace(title) == "" {
		title = "Untitled"
	}
	return filepath.Join(proj, fmt.Sprintf("%s %s [%s].md", chatDate(m.Created), chatFileName(title, 80), shortID(m.ID)))
}

// --- conversation JSON and Markdown --------------------------------------------------------

type ccFile struct {
	FileName string `json:"file_name"`
}

type ccContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type ccMsg struct {
	UUID        string      `json:"uuid"`
	Sender      string      `json:"sender"`
	Text        string      `json:"text"`
	Index       int         `json:"index"`
	Parent      string      `json:"parent_message_uuid"`
	Content     []ccContent `json:"content"`
	Attachments []ccFile    `json:"attachments"`
	Files       []ccFile    `json:"files"`
	FilesV2     []ccFile    `json:"files_v2"`
}

type ccConv struct {
	UUID        string  `json:"uuid"`
	Name        string  `json:"name"`
	Model       string  `json:"model"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	CurrentLeaf string  `json:"current_leaf_message_uuid"`
	Messages    []ccMsg `json:"chat_messages"`
}

// visibleBranch is the conversation as the user sees it: from the current leaf back to the
// root through the parents (edits and retries leave other branches in the tree).
func visibleBranch(c ccConv) []ccMsg {
	byID := map[string]int{}
	for i, m := range c.Messages {
		byID[m.UUID] = i
	}
	if i, ok := byID[c.CurrentLeaf]; ok && c.CurrentLeaf != "" {
		var out []ccMsg
		seen := map[string]bool{}
		for {
			m := c.Messages[i]
			if seen[m.UUID] {
				break
			}
			seen[m.UUID] = true
			out = append(out, m)
			j, ok := byID[m.Parent]
			if !ok {
				break
			}
			i = j
		}
		for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
			out[l], out[r] = out[r], out[l]
		}
		return out
	}
	out := append([]ccMsg(nil), c.Messages...)
	sort.SliceStable(out, func(a, b int) bool { return out[a].Index < out[b].Index })
	return out
}

// toolLine is one line describing a tool call: its name and its most telling input.
func toolLine(c ccContent) string {
	var in map[string]any
	json.Unmarshal(c.Input, &in)
	what := ""
	for _, k := range []string{"query", "url", "command", "path", "title", "description", "name", "prompt"} {
		if s, ok := in[k].(string); ok && strings.TrimSpace(s) != "" {
			what = s
			break
		}
	}
	if what == "" && len(c.Input) > 0 && string(c.Input) != "{}" && string(c.Input) != "null" {
		what = string(c.Input)
	}
	what = strings.TrimSpace(spaceRun.ReplaceAllString(what, " "))
	if utf8.RuneCountInString(what) > 120 {
		what = string([]rune(what)[:117]) + "..."
	}
	name := c.Name
	if name == "" {
		name = "tool"
	}
	if what == "" {
		return "> Tool: " + name
	}
	return "> Tool: " + name + " — " + what
}

func messageBody(m ccMsg) string {
	if len(m.Content) == 0 {
		return strings.TrimSpace(m.Text)
	}
	var parts []string
	for _, c := range m.Content {
		switch c.Type {
		case "text":
			if t := strings.TrimSpace(c.Text); t != "" {
				parts = append(parts, t)
			}
		case "tool_use", "server_tool_use":
			parts = append(parts, toolLine(c))
		}
	}
	return strings.Join(parts, "\n\n")
}

func messageFiles(m ccMsg) []string {
	var names []string
	seen := map[string]bool{}
	for _, list := range [][]ccFile{m.Attachments, m.Files, m.FilesV2} {
		for _, f := range list {
			if f.FileName != "" && !seen[f.FileName] {
				seen[f.FileName] = true
				names = append(names, f.FileName)
			}
		}
	}
	return names
}

// renderChatMarkdown is the file for one chat: a header, then the turns in order.
func renderChatMarkdown(m chatMeta, c ccConv) string {
	var b strings.Builder
	title := c.Name
	if strings.TrimSpace(title) == "" {
		title = m.Title
	}
	if strings.TrimSpace(title) == "" {
		title = "Untitled"
	}
	proj := m.Project
	if proj == "" {
		proj = "No project"
	}
	model := c.Model
	if model == "" {
		model = m.Model
	}
	created, updated := firstNonEmpty(c.CreatedAt, m.Created), firstNonEmpty(c.UpdatedAt, m.Updated)
	fmt.Fprintf(&b, "# %s\n\n", strings.TrimSpace(title))
	fmt.Fprintf(&b, "- URL: https://claude.ai/chat/%s\n- Created: %s\n- Updated: %s\n- Project: %s\n- Model: %s\n",
		m.ID, created, updated, proj, orDash(model))
	for _, msg := range visibleBranch(c) {
		who := "Claude"
		if msg.Sender == "human" {
			who = "You"
		}
		fmt.Fprintf(&b, "\n## %s\n\n", who)
		if body := messageBody(msg); body != "" {
			b.WriteString(body + "\n")
		}
		if files := messageFiles(msg); len(files) > 0 {
			fmt.Fprintf(&b, "\nAttachments: %s\n", strings.Join(files, ", "))
		}
	}
	return b.String()
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// writeFileAtomic writes via a temp file and rename, so a reader never sees half a chat.
func writeFileAtomic(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return renameRetry(tmp, p)
}
