package main

import (
	"bufio"
	"bytes"
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

const serverInstructions = `Claudessistent does file work locally on this Windows PC so it doesn't have to pass through the chat. Prefer it over moving, listing or reading files step by step: scan and find to look around, read for just the part of a file you need, run_job for copy/move/sync/delete/rename of any number of files, local_ai to have the PC's own model read or sort many documents. Dry-run anything that deletes, renames or replaces. Results are short summaries; full logs stay on the PC.`

func serveMCP(in io.Reader, out io.Writer) {
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
			"serverInfo":      map[string]any{"name": "claudessistent", "version": appVersion},
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
		text, err := callTool(p.Name, p.Arguments)
		isErr := err != nil
		if isErr {
			text = err.Error()
		}
		resp["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
	default:
		resp["error"] = map[string]any{"code": -32601, "message": "method not found: " + req.Method}
	}
	return resp
}

func callTool(name string, args json.RawMessage) (out string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("internal error: %v", r)
		}
	}()
	switch name {
	case "scan":
		return toolScan(args)
	case "find":
		return toolFind(args)
	case "read":
		return toolRead(args)
	case "run_job":
		return toolRunJob(args)
	case "job":
		return toolJob(args)
	case "saved":
		return toolSaved(args)
	case "local_ai":
		return toolLocalAI(args)
	}
	return "", fmt.Errorf("unknown tool %q", name)
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
	return formatState(&s), nil
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
	return "", errors.New("action must be status, list, cancel or undo")
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
	j, err := newJob(JobSpec{Name: "local AI " + req.Action, Kind: "ai", AI: &req, DryRun: a.DryRun, Source: "chat"})
	if err != nil {
		return "", err
	}
	j.start()
	waitJob(j, waitSecs(a.Wait, 40, 50))
	s := j.snapshot()
	return formatState(&s), nil
}

var toolDefs = []json.RawMessage{
	json.RawMessage(`{"name":"scan","description":"Overview of a folder on this PC: file count, total size, types, date range, largest files, subfolders, sample names. Use instead of listing files.","inputSchema":{"type":"object","properties":{"path":{"type":"string","description":"Absolute folder or file path (%VARS% and ~ work)"},"recursive":{"type":"boolean","description":"Include subfolders (default true)"},"include":{"type":"string","description":"Only matching names, e.g. *.jpg;*.png"},"exclude":{"type":"string"}},"required":["path"]}}`),
	json.RawMessage(`{"name":"find","description":"Find files on this PC by name, size, date and/or text inside them (text, code, Word, Excel, PowerPoint). Returns only matches with size, date and matching lines.","inputSchema":{"type":"object","properties":{"path":{"type":"array","items":{"type":"string"},"description":"Folders to search"},"name":{"type":"string","description":"Wildcards (*.pdf;*.docx) or plain text the name contains"},"text":{"type":"string","description":"Text to look for inside files"},"regex":{"type":"boolean"},"case_sensitive":{"type":"boolean"},"include":{"type":"string"},"exclude":{"type":"string"},"min_size":{"type":"string"},"max_size":{"type":"string"},"newer_than":{"type":"string"},"older_than":{"type":"string"},"recursive":{"type":"boolean"},"limit":{"type":"integer","description":"Default 50"}},"required":["path"]}}`),
	json.RawMessage(`{"name":"read","description":"Read only part of a file on this PC: info, head, tail, lines, grep, or text (Word/Excel/PowerPoint/HTML come back as plain text). Lines are numbered; output is capped.","inputSchema":{"type":"object","properties":{"path":{"type":"string"},"mode":{"type":"string","enum":["info","head","tail","lines","grep","text"]},"lines":{"type":"integer","description":"How many lines (default 40)"},"from_line":{"type":"integer"},"pattern":{"type":"string","description":"For grep (case-insensitive)"},"regex":{"type":"boolean"},"context":{"type":"integer","description":"Lines around each grep hit (default 2)"},"max_chars":{"type":"integer","description":"Default 6000"}},"required":["path"]}}`),
	json.RawMessage(`{"name":"run_job","description":"Run file operations on this PC as a background job; one call covers any number of files. Steps run in order. Returns the summary if done within wait_seconds, otherwise a job id for the job tool.\nOps:\n- copy/move {from, to}: a folder in from lands inside to (like Explorer); contents_only takes just what's inside; flatten drops subfolders. conflict: keep_both (default; identical files are skipped), skip, newer, overwrite (old version goes to the Recycle Bin). Same-drive moves are instant.\n- sync {from: one folder, to}: copies new and changed files; delete_extra also removes files that aren't in from.\n- delete {from}: to the Recycle Bin (a holding folder on USB/network drives); never permanent. A folder with no filters goes as one item.\n- rename {from, rules}: rules apply in order; each may use replace+with (regex, ignore_case), template, case (lower|upper|title|sentence), prefix, suffix, ext, ext_case, trim. on_conflict: stop (default, renames nothing if names clash), number, skip. counter: folder (default) or global; sort: name|date|taken|size; start. folders:true renames subfolders instead of files. recursive defaults to false for rename.\n- mkdir {to}.\nFilters on any step: include/exclude (wildcards separated by ;), recursive (default true), min_size/max_size (10MB), newer_than/older_than (30d, 6m, 1y or a date).\nTokens in rename template/prefix/suffix and in copy/move to: {name} {ext} {file} {parent} {type} {n} {n:3} {date:YYYY-MM-DD} {created:...} {taken:YYYY-MM-DD_hhmmss} (photo/video date, else modified). Format letters: YYYY YY MMM MM DD hh mm ss.\nverify: size (default), hash or none.\nDry-run first for delete, rename, overwrite and sync with delete_extra: it returns counts, examples and name clashes without changing anything.","inputSchema":{"type":"object","properties":{"steps":{"type":"array","items":{"type":"object","properties":{"op":{"type":"string","enum":["copy","move","sync","delete","rename","mkdir"]},"from":{"type":"array","items":{"type":"string"},"description":"Files, folders or wildcards like D:\\Pics\\*.jpg"},"to":{"type":"string"},"contents_only":{"type":"boolean"},"flatten":{"type":"boolean"},"conflict":{"type":"string","enum":["keep_both","skip","newer","overwrite"]},"delete_extra":{"type":"boolean"},"rules":{"type":"array","items":{"type":"object","properties":{"replace":{"type":"string"},"with":{"type":"string"},"regex":{"type":"boolean"},"ignore_case":{"type":"boolean"},"template":{"type":"string"},"case":{"type":"string","enum":["lower","upper","title","sentence"]},"prefix":{"type":"string"},"suffix":{"type":"string"},"ext":{"type":"string"},"ext_case":{"type":"string","enum":["lower","upper"]},"trim":{"type":"boolean"}}}},"on_conflict":{"type":"string","enum":["stop","number","skip"]},"counter":{"type":"string","enum":["folder","global"]},"sort":{"type":"string","enum":["name","date","taken","size"]},"start":{"type":"integer"},"folders":{"type":"boolean"},"include":{"type":"string"},"exclude":{"type":"string"},"recursive":{"type":"boolean"},"min_size":{"type":"string"},"max_size":{"type":"string"},"newer_than":{"type":"string"},"older_than":{"type":"string"},"verify":{"type":"string","enum":["size","hash","none"]},"workers":{"type":"integer"},"remove_empty_dirs":{"type":"boolean"}},"required":["op"]}},"saved":{"type":"string","description":"Run a saved job by name instead of steps"},"name":{"type":"string","description":"Label for the job"},"dry_run":{"type":"boolean"},"wait_seconds":{"type":"integer","description":"Default 15, max 50"}}}}`),
	json.RawMessage(`{"name":"job","description":"Check, list, cancel or undo jobs. status without id = latest job; wait_seconds waits for a running job. undo puts moved/renamed files back, removes the copies a job made (to the Recycle Bin) and restores what it deleted or replaced; dry_run previews it.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["status","list","cancel","undo"]},"id":{"type":"string"},"dry_run":{"type":"boolean"},"wait_seconds":{"type":"integer"}}}}`),
	json.RawMessage(`{"name":"saved","description":"Saved jobs the user can re-run by double-clicking them in the Claudessistent Jobs folder on the Desktop, or that you run with run_job saved=name. action: list, show, save (name, steps like run_job, description), remove.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["list","show","save","remove"]},"name":{"type":"string"},"description":{"type":"string"},"steps":{"type":"array","items":{"type":"object"}}},"required":["action"]}}`),
	json.RawMessage(`{"name":"local_ai","description":"Have the local AI model on this PC read files so their text never comes through the chat. The app starts the model when needed and shuts it down afterwards. ask: answer a question from the files and name which files; summarize: a few lines per file; classify: pick one of categories per file and optionally move files into move_to\\<category> (dry_run to preview); models: what's available. Reads text, code, Word, Excel, PowerPoint and HTML; not PDFs yet. Local models can be wrong: check key facts with read on the file it names.","inputSchema":{"type":"object","properties":{"action":{"type":"string","enum":["ask","summarize","classify","models"]},"paths":{"type":"array","items":{"type":"string"},"description":"Files or folders"},"question":{"type":"string"},"categories":{"type":"array","items":{"type":"string"}},"move_to":{"type":"string"},"include":{"type":"string"},"recursive":{"type":"boolean"},"max_files":{"type":"integer","description":"Default 200"},"detail":{"type":"string","enum":["brief","normal"]},"model":{"type":"string"},"dry_run":{"type":"boolean"},"wait_seconds":{"type":"integer","description":"Default 40, max 50"}},"required":["action"]}}`),
}
