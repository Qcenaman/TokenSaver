package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

type rpcReq struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

const serverInstructions = `TokenSaver works on this Windows PC and returns short results, so use it instead of built-in file, web-fetch, browser or computer-use tools whenever it can do the job. scan/find to look around; read for just the part you need (Office, PDF, and images or scans via OCR; image mode for a small picture); run_job for file operations on any number of files; run for commands (exit code and error lines only); system for PC state; web for search and page facts; browser for JavaScript or signed-in pages; window to read on-screen text instead of screenshots; local_ai to have the PC's own model read, describe or sort many files and images. Dry-run deletes, renames and overwrites.`

func serveMCP(in io.Reader, out io.Writer) {
	resumeNotifier()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var wmu sync.Mutex
	send := func(v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		out.Write(append(b, '\n'))
		wmu.Unlock()
	}
	var wg sync.WaitGroup
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcReq
		if err := json.Unmarshal(line, &req); err != nil {
			send(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": "parse error"}})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		wg.Add(1)
		go func(req rpcReq) {
			defer wg.Done()
			send(handleRPC(req))
		}(req)
	}
	wg.Wait()
	waitAllJobs()
}

func handleRPC(req rpcReq) map[string]any {
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := p.ProtocolVersion
		if v == "" {
			v = "2025-06-18"
		}
		resp["result"] = map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "tokensaver", "version": appVersion},
			"instructions":    serverInstructions,
		}
	case "ping":
		resp["result"] = map[string]any{}
	case "tools/list":
		resp["result"] = map[string]any{"tools": toolDefs}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			resp["error"] = map[string]any{"code": -32602, "message": "bad params"}
			return resp
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		text, imgs, err := callTool(p.Name, p.Arguments)
		isErr := err != nil
		if isErr {
			text, imgs = err.Error(), nil
		}
		content := []map[string]any{{"type": "text", "text": text}}
		for _, im := range imgs {
			content = append(content, map[string]any{"type": "image", "data": base64.StdEncoding.EncodeToString(im.data), "mimeType": im.mime})
		}
		resp["result"] = map[string]any{"content": content, "isError": isErr}
	default:
		resp["error"] = map[string]any{"code": -32601, "message": "method not found: " + req.Method}
	}
	return resp
}

type toolImage struct {
	data []byte
	mime string
}

func callTool(name string, args json.RawMessage) (out string, imgs []toolImage, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	switch name {
	case "scan":
		out, err = toolScan(args)
	case "find":
		out, err = toolFind(args)
	case "read":
		return toolRead(args)
	case "run_job":
		out, err = toolRunJob(args)
	case "job":
		out, err = toolJob(args)
	case "saved":
		out, err = toolSaved(args)
	case "local_ai":
		out, err = toolLocalAI(args)
	case "run":
		out, err = toolRun(args)
	case "system":
		out, err = toolSystem(args)
	case "web":
		out, err = toolWeb(args)
	case "window":
		out, err = toolWindow(args)
	case "browser":
		out, err = toolBrowser(args)
	default:
		err = fmt.Errorf("unknown tool %q", name)
	}
	return
}

func waitSecs(p *int, def, maxV int) int {
	if p == nil {
		return def
	}
	return min(max(*p, 0), maxV)
}

func toolRunJob(raw json.RawMessage) (string, error) {
	var a struct {
		Steps  []Step `json:"steps"`
		Saved  string `json:"saved"`
		Name   string `json:"name"`
		DryRun bool   `json:"dry_run"`
		Notify bool   `json:"notify"`
		Wait   *int   `json:"wait_seconds"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	if a.Saved != "" {
		sj, err := loadSaved(a.Saved)
		if err != nil {
			return "", err
		}
		a.Steps, a.Name = sj.Steps, sj.Name
	}
	if err := validateSteps(a.Steps); err != nil {
		return "", err
	}
	j, err := newJob(JobSpec{Name: a.Name, Kind: "files", Steps: a.Steps, DryRun: a.DryRun, Source: "chat"})
	if err != nil {
		return "", err
	}
	j.start()
	waitJob(j, waitSecs(a.Wait, 15, 50))
	s := j.snapshot()
	return withNotify(formatState(&s), &s, a.Notify), nil
}

func toolJob(raw json.RawMessage) (string, error) {
	var a struct {
		Action string `json:"action"`
		ID     string `json:"id"`
		DryRun bool   `json:"dry_run"`
		Wait   *int   `json:"wait_seconds"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	switch strings.ToLower(a.Action) {
	case "list":
		return listJobs(15), nil
	case "cancel":
		id, err := resolveJobID(a.ID)
		if err != nil {
			return "", err
		}
		return cancelJob(id), nil
	case "undo":
		if a.ID == "" {
			return "", errors.New("undo needs the job id")
		}
		id, err := resolveJobID(a.ID)
		if err != nil {
			return "", err
		}
		j, err := newJob(JobSpec{Name: "undo " + id, Kind: "undo", UndoOf: id, DryRun: a.DryRun, Source: "chat"})
		if err != nil {
			return "", err
		}
		j.start()
		waitJob(j, waitSecs(a.Wait, 20, 50))
		s := j.snapshot()
		return formatState(&s), nil
	case "notify":
		id, err := resolveJobID(a.ID)
		if err != nil {
			return "", err
		}
		s, err := loadState(id)
		if err != nil {
			return "", err
		}
		return withNotify(formatState(s), s, true), nil
	case "", "status":
		id, err := resolveJobID(a.ID)
		if err != nil {
			return "", err
		}
		if v, ok := running.Load(id); ok {
			waitJob(v.(*Job), waitSecs(a.Wait, 0, 50))
		}
		s, err := loadState(id)
		if err != nil {
			return "", err
		}
		return formatState(s), nil
	}
	return "", errors.New("action must be status, list, cancel, undo or notify")
}

func toolSaved(raw json.RawMessage) (string, error) {
	var a struct {
		Action      string `json:"action"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Steps       []Step `json:"steps"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	switch strings.ToLower(a.Action) {
	case "", "list":
		list := listSaved()
		if len(list) == 0 {
			return "No saved jobs yet.", nil
		}
		var b strings.Builder
		for _, s := range list {
			fmt.Fprintf(&b, "%s — %s", s.Name, clip(describeSteps(s.Steps), 160))
			if s.Description != "" {
				b.WriteString(" (" + s.Description + ")")
			}
			b.WriteString("\n")
		}
		return strings.TrimRight(b.String(), "\n"), nil
	case "show":
		s, err := loadSaved(a.Name)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(s.Steps)
		return s.Name + ": " + string(b), nil
	case "save":
		p, err := saveJob(SavedJob{Name: a.Name, Description: a.Description, Steps: a.Steps})
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Saved %q. Run it with run_job saved=%q.", strings.TrimSpace(a.Name), strings.TrimSpace(a.Name))
		if p != "" {
			msg += " The user can run it by double-clicking " + p
		}
		return msg, nil
	case "remove":
		if err := removeSaved(a.Name); err != nil {
			return "", err
		}
		return "Removed saved job " + a.Name + ".", nil
	}
	return "", errors.New("action must be list, show, save or remove")
}

func toolLocalAI(raw json.RawMessage) (string, error) {
	var a struct {
		AIRequest
		DryRun bool `json:"dry_run"`
		Notify bool `json:"notify"`
		Wait   *int `json:"wait_seconds"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", err
	}
	req := a.AIRequest
	req.Action = strings.ToLower(req.Action)
	switch req.Action {
	case "models":
		return aiModelsText(), nil
	case "ask":
		if req.Question == "" {
			return "", errors.New("ask needs question")
		}
	case "summarize":
	case "classify":
		if len(req.Categories) < 2 {
			return "", errors.New("classify needs at least 2 categories")
		}
	default:
		return "", errors.New("action must be ask, summarize, classify or models")
	}
	if len(req.Paths) == 0 {
		return "", errors.New("give paths: files or folders to read")
	}
	if !req.Force {
		if w := gpuWarning(); w != "" {
			return w, nil
		}
	}
	j, err := newJob(JobSpec{Name: "local AI " + req.Action, Kind: "ai", AI: &req, DryRun: a.DryRun, Source: "chat"})
	if err != nil {
		return "", err
	}
	j.start()
	waitJob(j, waitSecs(a.Wait, 40, 50))
	s := j.snapshot()
	return withNotify(formatState(&s), &s, a.Notify), nil
}

var toolDefs = []json.RawMessage{
	json.RawMessage(`{"name":"scan","description":"Overview of a folder on this PC: file count, total size, types, date range, largest files, subfolders, sample names. Use instead of listing files.","inputSchema":{"type":"object","properties":{"path":{"type":"string","description":"Absolute folder or file path (%VARS% and ~ work)"},"recursive":{"type":"boolean","description":"Include subfolders (default true)"},"include":{"type":"string","description":"Only matching names, e.g. *.jpg;*.png"},"exclude":{"type":"string"}},"required":["path"]}}`),
	json.RawMessage(`{"name":"find","description":"Find files on this PC by name, size, date and/or text inside them (text, code, Word, Excel, PowerPoint, PDF). Returns only matches with size, date and matching lines. Searches many folders at once; Program Files can be searched and read (read-only).","inputSchema":{"type":"object","properties":{"path":{"type":"array","items":{"type":"string"},"description":"Folders to search"},"name":{"type":"string","description":"Wildcards (*.pdf;*.docx) or plain text the name contains"},"text":{"type":"string","description":"Text to look for inside files"},"regex":{"type":"boolean"},"case_sensitive":{"type":"boolean"},"include":{"type":"string"},"exclude":{"type":"string"},"min_size":{"type":"string"},"max_size":{"type":"string"},"newer_than":{"type":"string"},"older_than":{"type":"string"},"recursive":{"type":"boolean"},"limit":{"type":"integer","description":"Default 50"},"all":{"type":"boolean","description":"Also search cache/tool folders (node_modules, .git, venvs, AppData Temp), skipped by default"}},"required":["path"]}}`),
	json.RawMessage(`{"name":"read","description":"Read only part of a file on this PC: info, head, tail, lines, grep, or text (Word/Excel/PowerPoint/HTML/PDF come back as plain text; scanned PDF pages and pictures are read with OCR). pages: only these PDF pages, e.g. 3-5. image: a small copy of a picture or PDF page to look at (max_px, default 800). Lines are numbered; output is capped. values: for a JSON file or a folder of them, every value of the given keys with counts, plus every string ending in ext; e.g. keys [cnr_id, aux_id, type] with ext .safetensors;.gguf;.ckpt;.pth lists a ComfyUI workflow's custom nodes and models.","inputSchema":{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string","enum":["info","head","tail","lines","grep","text","image","values"]},"lines":{"type":"integer","description":"How many lines (default 40)"},"from_line":{"type":"integer"},"pattern":{"type":"string","description":"For grep (case-insensitive)"},"regex":{"type":"boolean"},"context":{"type":"integer","description":"Lines around each grep hit (default 2)"},"max_chars":{"type":"integer","description":"Default 6000"},"keys":{"type":"array","items":{"type":"string"},"description":"For values"},"ext":{"type":"string","description":"For values, e.g. .safetensors;.gguf"},"pages":{"type":"string","description":"PDF pages, e.g. 3 or 3-5,8"},"max_px":{"type":"integer","description":"For image: longest side (default 800)"}},"required":["path"]}}`),
	json.RawMessage(`{"name":"run_job","description":"Run file operations on this PC as a background job; one call covers any number of files. Steps run in order. Returns the summary if done within wait_seconds, otherwise a job id for the job tool.\nOps:\n- copy/move {from, to}: a folder in from lands inside to (like Explorer); contents_only takes just what's inside; flatten drops subfolders. conflict: keep_both (default; identical files are skipped), skip, newer, overwrite (old version goes to the Recycle Bin). Same-drive moves are instant.\n- sync {from: one folder, to}: copies new and changed files; delete_extra also removes files that aren't in from.\n- delete {from}: to the Recycle Bin (a holding folder on USB/network drives); never permanent. A folder with no filters goes as one item.\n- rename {from, rules}: rules apply in order; each may use replace+with (regex, ignore_case), template, case (lower|upper|title|sentence), prefix, suffix, ext, ext_case, trim. on_conflict: stop (default, renames nothing if names clash), number, skip. counter: folder (default) or global; sort: name|date|taken|size; start. folders:true renames subfolders instead of files. recursive defaults to false for rename.\n- mkdir {to}.\n- download {from: URLs, to: folder}: resumes, skips files already there, name sets the file name (one URL), sha256 checks it; uses HF_TOKEN / CIVITAI_TOKEN env vars for gated files.\n- extract {from: archives, to}: .zip built in (undoable); 7z/rar/tar via 7-Zip or Windows tar.\n- edit {from: text files, edits:[{find, replace, regex, count}], append}: each find must match or that file is left unchanged; keeps line endings and encoding; undoable.\n- write {to: file, content}: create or replace a text file (old version kept for undo).\nFilters on any step: include/exclude (wildcards separated by ;), recursive (default true), min_size/max_size (10MB), newer_than/older_than (30d, 6m, 1y or a date).\nTokens in rename template/prefix/suffix and in copy/move to: {name} {ext} {file} {parent} {type} {n} {n:3} {date:YYYY-MM-DD} {created:...} {taken:YYYY-MM-DD_hhmmss} (photo/video date, else modified). Format letters: YYYY YY MMM MM DD hh mm ss.\nverify: size (default), hash or none.\nDry-run first for delete, rename, overwrite and sync with delete_extra: it returns counts, examples and name clashes without changing anything.","inputSchema":{"type":"object","properties":{"steps":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["copy","move","sync","delete","rename","mkdir","download","extract","edit","write"]},"from":{"type":"array","items":{"type":"string"},"description":"Files, folders wildcards like D:\\Pics\\*.jpg, or URLs for download"},"to":{"type":"string"},"contents_only":{"type":"boolean"},"flatten":{"type":"boolean"},"conflict":{"type":"string","enum":["keep_both","skip","newer","overwrite"]},"delete_extra":{"type":"boolean"},"rules":{"type":"array","items":{"type":"object","properties":{"replace":{"type":"string"},"with":{"type":"string"},"regex":{"type":"boolean"},"ignore_case":{"type":"boolean"},"template":{"type":"string"},"case":{"type":"string","enum":["lower","upper","title","sentence"]},"prefix":{"type":"string"},"suffix":{"type":"string"},"ext":{"type":"string"},"ext_case":{"type":"string","enum":["lower","upper"]},"trim":{"type":"boolean"}}}},"on_conflict":{"type":"string","enum":["stop","number","skip"]},"counter":{"type":"string","enum":["folder","global"]},"sort":{"type":"string","enum":["name","date","taken","size"]},"start":{"type":"integer"},"folders":{"type":"boolean"},"include":{"type":"string"},"exclude":{"type":"string"},"recursive":{"type":"boolean"},"min_size":{"type":"string"},"max_size":{"type":"string"},"newer_than":{"type":"string"},"older_than":{"type":"string"},"verify":{"type":"string","enum":["size","hash","none"]},"workers":{"type":"integer"},"remove_empty_dirs":{"type":"boolean"},"name":{"type":"string"},"sha256":{"type":"string"},"edits":{"type":"array","items":{"type":"object","properties":{"find":{"type":"string"},"replace":{"type":"string"},"regex":{"type":"boolean"},"count":{"type":"integer"}},"required":["find"]}},"append":{"type":"string"},"content":{"type":"string"}},"required":["op"]}},"saved":{"type":"string","description":"Run a saved job by name instead of steps"},"name":{"type":"string","description":"Label for the job"},"dry_run":{"type":"boolean"},"wait_seconds":{"type":"integer","description":"Default 15, max 50"},"notify":{"type":"boolean","description":"If it's still running when this returns: TokenSaver signals you through GitHub when it ends (done or failed) and gives a shell command to wait for that without polling"}}}}`),
	json.RawMessage(`{"name":"job","description":"Check, list, cancel or undo jobs, or ask to be notified when one ends. status without id = latest job; wait_seconds waits for a running job. undo puts moved/renamed files back, removes the copies a job made (to the Recycle Bin) and restores what it deleted or replaced; dry_run previews it. notify: when job id ends (done, failed, canceled or interrupted) TokenSaver signals it through GitHub and returns a shell command for your workspace that waits for that signal without polling the PC or spending tokens; use it instead of reminders or repeated status checks.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["status","list","cancel","undo","notify"]},"id":{"type":"string"},"dry_run":{"type":"boolean"},"wait_seconds":{"type":"integer"}}}}`),
	json.RawMessage(`{"name":"saved","description":"Saved jobs the user can re-run by double-clicking them in D:\\Claude Apps\\TokenSaver\\Saved Jobs, or that you run with run_job saved=name. action: list, show, save (name, steps like run_job, description), remove.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["list","show","save","remove"]},"name":{"type":"string"},"description":{"type":"string"},"steps":{"type":"array","items":{"type":"object"}}},"required":["action"]}}`),
	json.RawMessage(`{"name":"local_ai","description":"Have the local AI model on this PC read files so their text never comes through the chat. The app starts the model when needed and shuts it down afterwards. ask: answer a question from the files and name which files; summarize: a few lines per file; classify: pick one of categories per file and optionally move files into move_to\\<category> (dry_run to preview); models: what's available. Reads text, code, Office, PDF and pictures (a vision model looks at pictures itself; otherwise their OCR text is used). For web pages use web. Local models can be wrong: check key facts with read on the file it names. Before loading the model it checks whether the GPU is busy (ComfyUI, LoRA training with AI Toolkit, or heavy VRAM use); if so it loads nothing and returns a WARNING: tell the user and only retry with force:true if they agree.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["ask","summarize","classify","models"]},"paths":{"type":"array","items":{"type":"string"},"description":"Files or folders"},"question":{"type":"string"},"categories":{"type":"array","items":{"type":"string"}},"move_to":{"type":"string"},"include":{"type":"string"},"recursive":{"type":"boolean"},"max_files":{"type":"integer","description":"Default 200"},"detail":{"type":"string","enum":["brief","normal"]},"model":{"type":"string"},"dry_run":{"type":"boolean"},"force":{"type":"boolean","description":"Load the model even though the GPU is busy; only after the user agreed"},"wait_seconds":{"type":"integer","description":"Default 40, max 50"},"notify":{"type":"boolean","description":"If it's still running when this returns: TokenSaver signals you through GitHub when it ends (done or failed) and gives a shell command to wait for that without polling"}},"required":["action"]}}`),
	json.RawMessage(`{"name":"run","description":"Run a command on this PC (cmd by default, or powershell) as a background job. Returns only what matters: exit code, error lines (Python tracebacks condensed to the exception and where it happened) and the last lines; the full output stays in a log on the PC (read it with read). For installs (pip, uv, git, winget, installers with silent flags), builds, scripts and starting programs. Nothing can be typed into it, so pass flags like -y / --yes. wait_for returns as soon as a line matches (regex), e.g. a server's ready message, and leaves it running; keep_running removes the timeout. Long jobs keep going: check with job status, stop with job cancel. elevated: true runs it as administrator through the elevated helper, with no UAC prompt; use it only for commands that need admin. The helper refuses commands that delete or change system state (del, rm, Remove-Item, reg add/delete, sc/net stop, Stop-Service, format, diskpart, bcdedit, takeown, icacls, uninstalls, firewall/Defender changes…) unless confirmed: true — ask the user in chat first and set confirmed only after they say yes to that exact command. Every elevated command is logged in D:\\Claude Apps\\TokenSaver\\admin-log\\elevated.log.","inputSchema":{"type":"object","properties":{"command":{"type":"string"},"elevated":{"type":"boolean","description":"Run as administrator via the elevated helper"},"confirmed":{"type":"boolean","description":"Only after the user said yes in chat to this destructive elevated command"},"shell":{"type":"string","enum":["cmd","powershell","pwsh"]},"cwd":{"type":"string","description":"Folder to run in"},"env":{"type":"object","additionalProperties":{"type":"string"},"description":"Extra environment variables"},"timeout_minutes":{"type":"integer","description":"Default 60; 0 = none"},"keep_running":{"type":"boolean","description":"For servers: no timeout"},"wait_for":{"type":"string","description":"Regex; return when output matches"},"wait_seconds":{"type":"integer","description":"Default 30, max 50"},"tail":{"type":"integer","description":"Last lines to show (default 15)"},"name":{"type":"string"},"notify":{"type":"boolean","description":"If it's still running when this returns: TokenSaver signals you through GitHub when it ends (done or failed) and gives a shell command to wait for that without polling"}},"required":["command"]}}`),
	json.RawMessage(`{"name":"system","description":"This PC's state. info: Windows version, CPU, RAM, free space on every drive, NVIDIA GPU, driver and highest CUDA it supports, and which tools are installed with versions (python, py, git, uv, conda, node, nvcc, ffmpeg, winget, 7-Zip). processes: running programs with memory and command line, filtered by text. ports: listening ports and the program on each. kill: stop processes (and their children) by pids, or by name plus optional match on the command line; system and Claude processes are refused.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["info","processes","ports","kill"]},"filter":{"type":"string"},"port":{"type":"integer"},"pids":{"type":"array","items":{"type":"integer"}},"name":{"type":"string"},"match":{"type":"string"}}}}`),
	json.RawMessage(`{"name":"web","description":"Search the web and read pages on this PC, so page text never comes through the chat. Use it instead of WebFetch, WebSearch or a browser whenever you only need facts from pages. search: titles, URLs and snippets for query. find: just the lines matching pattern (text, or regex:true), with line numbers and context, in urls or in the top pages results of query; the cheap way to pull a fact out of pages. read: a page's title, outline (headings with line numbers) and first lines; from_line and lines read any part. links: the page's links, filtered by pattern. ask: the local AI reads urls (or the top pages results of query) and answers question, naming the pages; it's a job, with the same GPU check as local_ai (force:true only after the user agrees). Pages are fetched without JavaScript (a note says when a page needs a browser) and cached 30 minutes (fresh:true refetches); read shows the cached text file, which read and find on files can also use. PDFs are read as text too.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["search","find","read","links","ask"]},"query":{"type":"string","description":"Search words (search; or find/ask instead of urls)"},"urls":{"type":"array","items":{"type":"string"},"description":"Pages for find/ask; read and links use the first"},"url":{"type":"string","description":"One page (read, links)"},"pattern":{"type":"string","description":"find: text to look for (case-insensitive); links: filter"},"regex":{"type":"boolean"},"context":{"type":"integer","description":"find: lines around each hit (default 1)"},"question":{"type":"string","description":"ask"},"pages":{"type":"integer","description":"How many search results to open for find (default 5) or ask (default 4), max 10"},"results":{"type":"integer","description":"search: how many results (default 8)"},"from_line":{"type":"integer"},"lines":{"type":"integer","description":"read: how many lines (default 40 with the outline, else 80)"},"max_chars":{"type":"integer","description":"Default 6000, max 30000"},"fresh":{"type":"boolean","description":"Refetch instead of using the 30-minute cache"},"model":{"type":"string"},"force":{"type":"boolean","description":"ask: load the model even though the GPU is busy; only after the user agreed"},"notify":{"type":"boolean","description":"If it's still running when this returns: TokenSaver signals you through GitHub when it ends (done or failed) and gives a shell command to wait for that without polling"},"wait_seconds":{"type":"integer","description":"ask: default 40, max 50"}},"required":["action"]}}`),
	json.RawMessage(`{"name":"window","description":"Read what's on screen as text instead of screenshots. list: open windows (title, program, pid, size; filter). text: a window's text from UI Automation, read-only, in screen order; buttons shown as [Name] (works for Chrome, Electron and Steam windows; falls back to OCR). ocr: Windows OCR of a window, or of an area x,y,w,h; text only. do: steps in a window, e.g. [{\"click\":\"Save\"},{\"type\":\"x\",\"into\":\"File name\"},{\"press\":\"enter\"},{\"wait\":1}], by the names text shows; then:\"text\" returns the window afterwards. copy: Ctrl+A, Ctrl+C in the window and return the text; the clipboard is put back. window: title, program name or pid (default: the front window). Long text is cut at max_chars (default 4000) with the full text saved to a file; summary:true has the local AI summarize it.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["list","text","ocr","copy","do"]},"window":{"type":"string"},"filter":{"type":"string"},"x":{"type":"integer"},"y":{"type":"integer"},"w":{"type":"integer"},"h":{"type":"integer"},"max_chars":{"type":"integer"},"summary":{"type":"boolean"},"steps":{"type":"array","items":{"type":"object"}},"then":{"type":"string","enum":["text"]}},"required":["action"]}}`),
	json.RawMessage(`{"name":"browser","description":"A real Chrome with its own TokenSaver profile (stays signed in), for pages that need JavaScript or a sign-in; everything comes back as text. open/text: go to url (optional) and return the page text. find: matching lines (pattern, context). links: links, filtered by pattern. click: a button or link by its text (target). fill: a field by label/placeholder/name (target) with value. press: a key (value, default Enter). wait: until pattern shows (wait seconds). show:true opens it visibly, e.g. so the user can sign in once. close. Use web instead for plain pages.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["open","text","find","links","click","fill","press","wait","close"]},"url":{"type":"string"},"target":{"type":"string"},"value":{"type":"string"},"pattern":{"type":"string"},"context":{"type":"integer"},"wait":{"type":"number"},"show":{"type":"boolean"},"max_chars":{"type":"integer"}},"required":["action"]}}`),
}
