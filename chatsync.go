package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// --- claude.ai over the browser ------------------------------------------------------------

// ccClient is a claude.ai tab of TokenSaver's browser; requests run as fetch() inside it.
type ccClient struct{ w *wsConn }

func openClaudeTab() (*ccClient, error) {
	if err := ensureBrowser(false); err != nil {
		return nil, err
	}
	pc, err := pageConn()
	if err != nil {
		return nil, err
	}
	res, err := pc.call("Target.createTarget", map[string]any{"url": "https://claude.ai/robots.txt"}, 30*time.Second)
	pc.c.Close()
	if err != nil {
		return nil, fmt.Errorf("couldn't open a claude.ai tab: %v", err)
	}
	var t struct {
		TargetID string `json:"targetId"`
	}
	json.Unmarshal(res, &t)
	w, err := wsDial(fmt.Sprintf("ws://127.0.0.1:%d/devtools/page/%s", cdpPort, t.TargetID))
	if err != nil {
		return nil, err
	}
	c := &ccClient{w: w}
	for i := 0; i < 60; i++ {
		if s, _ := w.eval("document.readyState", 3*time.Second); s == "complete" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	where, _ := w.eval("location.host + ' ' + document.title", 5*time.Second)
	if !strings.HasPrefix(where, "claude.ai") || isChallenge(where) {
		c.close()
		return nil, errChatsNeedSignIn
	}
	return c, nil
}

func (c *ccClient) close() {
	c.w.call("Page.close", map[string]any{}, 5*time.Second)
	c.w.c.Close()
}

func isChallenge(s string) bool {
	l := strings.ToLower(s)
	return strings.Contains(l, "just a moment") || strings.Contains(l, "challenge-platform") ||
		strings.Contains(l, "cf-chl") || strings.Contains(l, "attention required")
}

const jsFetchJSON = `(async (p) => {
  const r = await fetch(p, {credentials: 'include', headers: {'accept': 'application/json', 'anthropic-client-platform': 'web_claude_ai'}});
  return JSON.stringify({s: r.status, ct: r.headers.get('content-type') || '', ra: r.headers.get('retry-after') || '', t: await r.text()});
})`

// get fetches one API path and returns its body. Sign-in and Cloudflare checks are reported,
// never worked around; 429 waits and retries.
func (c *ccClient) get(path string) ([]byte, error) {
	wait := 5 * time.Second
	for attempt := 0; ; attempt++ {
		out, err := c.w.eval(jsFetchJSON+"("+jsStr(path)+")", 120*time.Second)
		if err != nil {
			return nil, err
		}
		var r struct {
			S  int    `json:"s"`
			CT string `json:"ct"`
			RA string `json:"ra"`
			T  string `json:"t"`
		}
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			return nil, errors.New("unreadable answer from the page")
		}
		html := strings.Contains(r.CT, "text/html")
		switch {
		case r.S == 200 && !html:
			return []byte(r.T), nil
		case r.S == 401 || (html && isChallenge(r.T)) || (r.S == 403 && html):
			return nil, errChatsNeedSignIn
		case r.S == 403 && strings.Contains(r.T, "account_session_invalid"):
			return nil, errChatsNeedSignIn
		case r.S == 429 && attempt < 6:
			d := wait
			var secs int
			if fmt.Sscan(r.RA, &secs); secs > 0 {
				d = time.Duration(min(secs, 120)) * time.Second
			}
			time.Sleep(d)
			wait = min(wait*2, time.Minute)
			continue
		}
		return nil, fmt.Errorf("HTTP %d", r.S)
	}
}

type ccOrg struct {
	UUID         string   `json:"uuid"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
}

type ccListItem struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	Model       string `json:"model"`
	ProjectUUID string `json:"project_uuid"`
	Project     *struct {
		UUID string `json:"uuid"`
		Name string `json:"name"`
	} `json:"project"`
}

func (c *ccClient) orgs() ([]ccOrg, error) {
	b, err := c.get("/api/organizations")
	if err != nil {
		return nil, err
	}
	var os []ccOrg
	if err := json.Unmarshal(b, &os); err != nil {
		return nil, errors.New("unexpected organizations answer")
	}
	var out []ccOrg
	for _, o := range os {
		if len(o.Capabilities) == 0 || containsFold(o.Capabilities, "chat") {
			out = append(out, o)
		}
	}
	return out, nil
}

// pagedList reads a list endpoint with limit/offset until a short or repeated page.
func (c *ccClient) pagedList(base string) ([]ccListItem, error) {
	const limit = 100
	var all []ccListItem
	seen := map[string]bool{}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	for off := 0; off < 100000; off += limit {
		b, err := c.get(fmt.Sprintf("%s%slimit=%d&offset=%d", base, sep, limit, off))
		if err != nil {
			return all, err
		}
		var page []ccListItem
		if err := json.Unmarshal(b, &page); err != nil {
			var wrapped struct {
				Data []ccListItem `json:"data"`
			}
			if json.Unmarshal(b, &wrapped) != nil {
				return all, errors.New("unexpected conversation list")
			}
			page = wrapped.Data
		}
		added := 0
		for _, it := range page {
			if !seen[it.UUID] {
				seen[it.UUID] = true
				all = append(all, it)
				added++
			}
		}
		if len(page) < limit || added == 0 {
			break
		}
	}
	return all, nil
}

// listChats lists every conversation of one organization, project chats included.
func (c *ccClient) listChats(org ccOrg) ([]chatMeta, error) {
	esc := url.PathEscape(org.UUID)
	projNames := map[string]string{}
	var projIDs []string
	if b, err := c.get("/api/organizations/" + esc + "/projects"); err == nil {
		var ps []struct {
			UUID string `json:"uuid"`
			Name string `json:"name"`
		}
		if json.Unmarshal(b, &ps) == nil {
			for _, p := range ps {
				projNames[p.UUID] = p.Name
				projIDs = append(projIDs, p.UUID)
			}
		}
	} else if errors.Is(err, errChatsNeedSignIn) {
		return nil, err
	}
	items, err := c.pagedList("/api/organizations/" + esc + "/chat_conversations")
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, it := range items {
		have[it.UUID] = true
	}
	for _, p := range projIDs {
		more, err := c.pagedList("/api/organizations/" + esc + "/projects/" + url.PathEscape(p) + "/conversations")
		if errors.Is(err, errChatsNeedSignIn) {
			return nil, err
		}
		for _, it := range more {
			if !have[it.UUID] {
				have[it.UUID] = true
				if it.ProjectUUID == "" {
					it.ProjectUUID = p
				}
				items = append(items, it)
			}
		}
	}
	out := make([]chatMeta, 0, len(items))
	for _, it := range items {
		m := chatMeta{ID: it.UUID, Org: org.UUID, Title: it.Name, Created: it.CreatedAt, Updated: it.UpdatedAt, Model: it.Model}
		pid := it.ProjectUUID
		if it.Project != nil {
			pid = firstNonEmpty(pid, it.Project.UUID)
			if it.Project.Name != "" {
				projNames[pid] = firstNonEmpty(projNames[pid], it.Project.Name)
			}
		}
		if pid != "" {
			m.Project = firstNonEmpty(projNames[pid], "Project "+shortID(pid))
		}
		out = append(out, m)
	}
	return out, nil
}

func (c *ccClient) conversation(m chatMeta) (ccConv, error) {
	var cv ccConv
	b, err := c.get("/api/organizations/" + url.PathEscape(m.Org) + "/chat_conversations/" + url.PathEscape(m.ID) +
		"?tree=True&rendering_mode=messages&render_all_tools=true")
	if err != nil {
		return cv, err
	}
	if err := json.Unmarshal(b, &cv); err != nil {
		return cv, errors.New("unexpected conversation answer")
	}
	return cv, nil
}

// --- the sync job ----------------------------------------------------------------------------

type chatsRequest struct {
	Full   bool `json:"full,omitempty"`
	DryRun bool `json:"dry_run,omitempty"`
	NoRag  bool `json:"no_rag,omitempty"`
}

func runChatsSync(j *Job) error {
	req := j.Spec.Chats
	if req == nil {
		req = &chatsRequest{}
	}
	root := chatsRoot()
	idx := loadChatIndex(root)

	j.setCurrent("opening claude.ai in TokenSaver's browser…")
	browserMu.Lock()
	cl, err := openClaudeTab()
	if err != nil {
		browserMu.Unlock()
		return err
	}
	closed := false
	done := func() {
		if !closed {
			cl.close()
			browserMu.Unlock()
			closed = true
		}
	}
	defer done()

	orgs, err := cl.orgs()
	if err != nil {
		return err
	}
	var remote []chatMeta
	seen := map[string]bool{}
	var orgNotes []string
	for _, o := range orgs {
		j.setCurrent("listing chats in " + firstNonEmpty(o.Name, shortID(o.UUID)) + "…")
		ms, err := cl.listChats(o)
		if errors.Is(err, errChatsNeedSignIn) {
			return err
		}
		if err != nil {
			orgNotes = append(orgNotes, fmt.Sprintf("organization %s skipped: %v", shortID(o.UUID), err))
			continue
		}
		for _, m := range ms {
			if !seen[m.ID] {
				seen[m.ID] = true
				remote = append(remote, m)
			}
		}
	}
	todo, unchanged := planChatSync(idx, remote, root, req.Full)
	newN := 0
	for _, t := range todo {
		if t.New {
			newN++
		}
	}
	if req.DryRun {
		done()
		j.setOutput(fmt.Sprintf("Claude chats (dry run): %d on claude.ai — would fetch %d new, %d updated; %d unchanged.\nFolder: %s%s",
			len(remote), newN, len(todo)-newN, unchanged, root, notesText(orgNotes)))
		return nil
	}

	var created, updated, failed int
	var changedPaths, failures []string
	for i, t := range todo {
		if j.ctx.Err() != nil {
			break
		}
		j.setCurrent(fmt.Sprintf("saving chat %d of %d", i+1, len(todo)))
		cv, err := cl.conversation(t.Meta)
		if errors.Is(err, errChatsNeedSignIn) {
			saveChatIndex(root, idx)
			return err
		}
		if err != nil {
			failed++
			failures = append(failures, shortID(t.Meta.ID)+": "+err.Error())
			continue
		}
		full := filepath.Join(root, t.Path)
		if t.OldPath != "" {
			if old := filepath.Join(root, t.OldPath); exists(old) && !exists(full) {
				os.MkdirAll(filepath.Dir(full), 0o755)
				renameRetry(old, full)
			}
		}
		if err := writeFileAtomic(full, []byte(renderChatMarkdown(t.Meta, cv))); err != nil {
			failed++
			failures = append(failures, shortID(t.Meta.ID)+": "+err.Error())
			continue
		}
		idx.Chats[t.Meta.ID] = chatIndexEntry{Path: t.Path, Updated: t.Meta.Updated, Title: t.Meta.Title, Project: t.Meta.Project}
		if t.New {
			created++
		} else {
			updated++
		}
		changedPaths = append(changedPaths, full)
		if (i+1)%10 == 0 {
			saveChatIndex(root, idx)
		}
		time.Sleep(150 * time.Millisecond) // gentle on claude.ai
	}
	done()
	idx.LastSync = time.Now()
	if err := saveChatIndex(root, idx); err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Claude chats: %d on claude.ai — new %d, updated %d, unchanged %d, failed %d.\nFolder: %s\nIndex: %s",
		len(remote), created, updated, unchanged, failed, root, chatsIndexPath(root))
	for k, p := range changedPaths {
		if k == 8 {
			fmt.Fprintf(&b, "\n  … and %d more", len(changedPaths)-8)
			break
		}
		b.WriteString("\n  " + p)
	}
	for k, f := range failures {
		if k == 5 {
			fmt.Fprintf(&b, "\n  … %d more failures", len(failures)-5)
			break
		}
		b.WriteString("\n  failed " + f)
	}
	b.WriteString(notesText(orgNotes))
	if !req.NoRag && j.ctx.Err() == nil {
		b.WriteString("\n" + chatsRagUpdate(j, root, created+updated > 0))
	}
	j.setOutput(b.String())
	return nil
}

func notesText(n []string) string {
	if len(n) == 0 {
		return ""
	}
	return "\n" + strings.Join(n, "\n")
}

func saveChatIndex(root string, idx chatIndex) error {
	idx.Version = 1
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	return writeJSONAtomic(chatsIndexPath(root), idx)
}

// chatsRagUpdate makes the chats folder a RAG root (once) and re-indexes it when files changed.
func chatsRagUpdate(j *Job, root string, changed bool) string {
	c := loadRagConfig()
	added := false
	if !containsFold(c.Roots, root) {
		c.Roots = append(c.Roots, root)
		if err := saveRagConfigOpt(c, false); err != nil {
			return "RAG: couldn't add " + root + " as a root: " + err.Error()
		}
		added = true
	}
	if !changed && !added {
		return "RAG: nothing new to index."
	}
	cpu, busy := ragAutoCPUFor(false, false, true)
	j.setCurrent("updating the RAG index of " + root + "…")
	rj, err := newJob(JobSpec{Name: "rag update (Claude chats)", Kind: "rag", Source: j.Spec.Source, Rag: &ragRequest{Paths: []string{root}, CPU: cpu, Busy: busy}})
	if err != nil {
		return "RAG: " + err.Error()
	}
	rj.start()
	select {
	case <-rj.done:
	case <-j.ctx.Done():
		return "RAG: update " + rj.ID + " still running"
	}
	s := rj.snapshot()
	msg := "RAG: update " + rj.ID + " " + s.Status
	if s.Error != "" {
		msg += " — " + s.Error
	}
	if added {
		msg = "RAG: added " + root + " as a root. " + msg
	}
	return msg
}

// --- tool --------------------------------------------------------------------------------------

func toolChats(raw json.RawMessage) (string, error) {
	var a struct {
		Action string `json:"action"`
		Full   bool   `json:"full"`
		DryRun bool   `json:"dry_run"`
		Notify bool   `json:"notify"`
		Query  string `json:"query"`
		K      int    `json:"k"`
		Wait   *int   `json:"wait_seconds"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	switch strings.ToLower(a.Action) {
	case "", "sync":
		j, err := startChatsSync("chat", chatsRequest{Full: a.Full, DryRun: a.DryRun})
		if err != nil {
			return "", err
		}
		waitJob(j, waitSecs(a.Wait, 40, 50))
		s := j.snapshot()
		return withNotify(formatState(&s), &s, a.Notify), nil
	case "status":
		return chatsStatus(), nil
	case "search":
		if strings.TrimSpace(a.Query) == "" {
			return "", errors.New("search needs query")
		}
		return ragSearch(ragSearchArgs{Query: a.Query, K: a.K, Under: chatsRoot(), Scope: "files"})
	case "schedule":
		return chatsSchedule()
	}
	return "", errors.New("action must be sync, status, search or schedule")
}

func startChatsSync(source string, req chatsRequest) (*Job, error) {
	j, err := newJob(JobSpec{Name: "claude chats sync", Kind: "chats", DryRun: req.DryRun, Source: source, Chats: &req})
	if err != nil {
		return nil, err
	}
	j.start()
	return j, nil
}

func chatsStatus() string {
	root := chatsRoot()
	idx := loadChatIndex(root)
	last := "never"
	if !idx.LastSync.IsZero() {
		last = idx.LastSync.Format("2006-01-02 15:04") + " (" + hd(time.Since(idx.LastSync)) + " ago)"
	}
	s := fmt.Sprintf("Claude chats: %d saved in %s, last sync %s\nScheduled sync: %s", len(idx.Chats), root, last, chatsTaskState())
	if containsFold(loadRagConfig().Roots, root) {
		s += "\nRAG: indexed folder"
	} else {
		s += "\nRAG: not a root yet (the next sync adds it)"
	}
	return s
}

// chatsShortcut is the double-click "Sync Claude chats.cmd" in the app folder.
func chatsShortcut() (string, error) {
	exe := installPath()
	if !exists(exe) {
		exe, _ = os.Executable()
	}
	p := filepath.Join(appRoot(), "Sync Claude chats.cmd")
	body := "@echo off\r\n\"" + exe + "\" chats sync\r\npause\r\n"
	return p, os.WriteFile(p, []byte(body), 0o644)
}

// chatsCLI: tokensaver chats sync [full] [dry] | status | schedule
func chatsCLI(args []string) int {
	act := "sync"
	if len(args) > 0 {
		act = strings.ToLower(args[0])
	}
	switch act {
	case "sync":
		req := chatsRequest{}
		for _, a := range args[1:] {
			switch strings.TrimLeft(strings.ToLower(a), "-") {
			case "full":
				req.Full = true
			case "dry", "dry_run", "dry-run":
				req.DryRun = true
			}
		}
		j, err := startChatsSync("schedule", req)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		<-j.done
		s := j.snapshot()
		fmt.Println(formatState(&s))
		if s.Status != "done" {
			return 1
		}
		return 0
	case "status":
		fmt.Println(chatsStatus())
		return 0
	case "schedule":
		out, err := chatsSchedule()
		fmt.Println(out)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		return 0
	}
	fmt.Println("usage: tokensaver chats sync [full] [dry] | status | schedule")
	return 2
}
