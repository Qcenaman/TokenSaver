package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// gpuUser is a program that is likely holding the GPU.
type gpuUser struct {
	what string
	pids []int
}

var gpuApps = []struct {
	what  string
	match func(name, cmd string) bool
}{
	{"LoRA training (AI Toolkit)", func(n, c string) bool {
		return strings.Contains(c, "ai-toolkit") || strings.Contains(c, "ai_toolkit") || (strings.Contains(c, `\toolkit\`) || strings.Contains(c, "/toolkit/")) && strings.Contains(c, "run.py")
	}},
	{"ComfyUI", func(n, c string) bool { return strings.Contains(n, "comfyui") || strings.Contains(c, "comfyui") }},
}

// gpuUsers finds the programs that are likely holding the GPU, plus a VRAM note
// when the GPU itself looks loaded. Both empty means the GPU looks free.
func gpuUsers() ([]*gpuUser, string) {
	var users []*gpuUser
	if ps, err := procs(); err == nil {
		found := map[string]*gpuUser{}
		for _, p := range ps {
			n, c := strings.ToLower(p.name), strings.ToLower(p.cmd)
			if strings.Contains(c, "tokensaver") || strings.Contains(n, "tokensaver") {
				continue
			}
			if what := aiServerProc(n, p.cmd); what != "" { // another model server: name it and its model
				u := &gpuUser{what: what, pids: []int{p.pid}}
				users = append(users, u)
				continue
			}
			for _, a := range gpuApps {
				if a.match(n, c) {
					u := found[a.what]
					if u == nil {
						u = &gpuUser{what: a.what}
						found[a.what] = u
						users = append(users, u)
					}
					u.pids = append(u.pids, p.pid)
					break
				}
			}
		}
	}
	if per := vramByPid(); len(per) > 0 {
		for _, u := range users {
			mb := 0
			for _, id := range u.pids {
				mb += per[id]
			}
			if mb > 0 {
				u.what += fmt.Sprintf(", %.1f GB VRAM", float64(mb)/1024)
			}
		}
	}
	vram := ""
	out, err := runQuiet(15*time.Second, "nvidia-smi", "--query-gpu=memory.used,memory.total,utilization.gpu", "--format=csv,noheader,nounits")
	if err == nil {
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			f := strings.Split(l, ",")
			if len(f) < 3 {
				continue
			}
			used, _ := strconv.ParseFloat(strings.TrimSpace(f[0]), 64)
			total, _ := strconv.ParseFloat(strings.TrimSpace(f[1]), 64)
			util, _ := strconv.Atoi(strings.TrimSpace(f[2]))
			// The desktop and a browser alone hold a few GB; more than that or a busy GPU means real work.
			if used > 6144 || util >= 40 || len(users) > 0 {
				vram = fmt.Sprintf("GPU has %.1f of %.1f GB VRAM in use and is %d%% busy", used/1024, total/1024, util)
			}
		}
	}
	return users, vram
}

func describeGPUUsers(users []*gpuUser, vram string) string {
	var reasons []string
	for _, u := range users {
		ids := make([]string, 0, len(u.pids))
		for i, id := range u.pids {
			if i >= 3 {
				ids = append(ids, "…")
				break
			}
			ids = append(ids, strconv.Itoa(id))
		}
		reasons = append(reasons, fmt.Sprintf("%s is running (pid %s)", u.what, strings.Join(ids, ", ")))
	}
	if vram != "" {
		reasons = append(reasons, vram)
	}
	return strings.Join(reasons, "; ")
}

// gpuBusy reports what is using the GPU right now. It returns "" when the GPU looks free.
func gpuBusy() string { return describeGPUUsers(gpuUsers()) }

// aiWouldLoad reports whether running a local AI job would load a model onto the GPU.
// A llama.cpp server that is already up is reused, so nothing new is loaded.
func aiWouldLoad() bool {
	if cfg.AIEndpoint == "" && cfg.AIScript != "" && exists(cfg.AIScript) {
		host, port := scriptAddr(cfg.AIScript)
		c := &aiClient{kind: "openai", base: fmt.Sprintf("http://%s:%d", host, port)}
		return !c.healthy()
	}
	// An ai_endpoint llama.cpp server that already answers needs no extra VRAM. (Ollama and
	// LM Studio list models that aren't loaded, so they keep the check.)
	if u := strings.TrimRight(cfg.AIEndpoint, "/"); u != "" && !strings.Contains(u, ":11434") && !strings.Contains(u, ":1234") {
		c := &aiClient{kind: "openai", base: u, hc: &http.Client{Timeout: 2 * time.Second}}
		if c.healthy() {
			return false
		}
	}
	return true
}

// gpuGate is the one GPU check every GPU job goes through. It only reminds the
// user: when something else holds the GPU it returns a reminder naming it, and
// the user picks gpu:"stop" (shut those programs down, then run) or
// gpu:"continue" (run anyway). load=false skips the check (nothing new would
// load onto the GPU). note is a line to show with the result after a stop.
func gpuGate(what, mode string, load bool) (reminder, note string) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if !load || mode == "continue" {
		return "", ""
	}
	users, vram := gpuUsers()
	if len(users) == 0 && vram == "" {
		return "", ""
	}
	if mode == "stop" {
		var pids []int
		for _, u := range users {
			pids = append(pids, u.pids...)
		}
		if len(pids) == 0 {
			return "", "GPU note: nothing known to stop (" + vram + "); went ahead anyway."
		}
		res, err := killProcs(pids, "", "")
		if err != nil {
			res = err.Error()
		}
		time.Sleep(2 * time.Second)
		return "", "Stopped for the GPU: " + strings.ReplaceAll(res, "\n", "; ")
	}
	stop := "shut them down first"
	if len(users) == 0 {
		stop = "not available (no known program to stop)"
	}
	return fmt.Sprintf("REMINDER: %s was not started because the GPU is in use: %s. Ask the user what to do: "+
		"stop (%s) or continue anyway (it may slow down or crash that work). Then call again with the same arguments plus "+
		"gpu:\"stop\" or gpu:\"continue\".", what, describeGPUUsers(users, vram), stop), ""
}

// gpuMode keeps the old force:true working as gpu:"continue".
func gpuMode(mode string, force bool) string {
	if force && mode == "" {
		return "continue"
	}
	return mode
}

// gpuNoted puts the stop note (if any) above a result.
func gpuNoted(note, out string) string {
	if note == "" {
		return out
	}
	return note + "\n" + out
}

var (
	modelArgRe  = regexp.MustCompile(`(?i)(?:^|\s)(?:-m|--model|-hf|--hf-repo|--models-preset)[ =]+(?:"([^"]+)"|(\S+))`)
	aiServerExe = regexp.MustCompile(`(?i)^(llama-server|ollama|ollama_llama_server|llama_server|lms|lm studio|koboldcpp|vllm)(\.exe)?$`)
)

// aiServerProc describes a local model server process ("" when p isn't one): the program and the
// model file it serves, so the user knows what to close. Embedding servers (TokenSaver's search
// index) are small and left out.
func aiServerProc(name, cmd string) string {
	n := strings.ToLower(name)
	if !aiServerExe.MatchString(n) && !strings.Contains(strings.ToLower(cmd), "llama-server") {
		return ""
	}
	if strings.Contains(cmd, "--embedding") || strings.Contains(strings.ToLower(n), "tokensaver") {
		return ""
	}
	what := name
	if m := modelArgRe.FindStringSubmatch(cmd); m != nil {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		what += " serving " + filepath.Base(strings.ReplaceAll(v, "\\", "/"))
	}
	return what
}

// vramByPid is each process's VRAM in MB, as nvidia-smi reports it (empty when it can't tell).
func vramByPid() map[int]int {
	out, err := runQuiet(15*time.Second, "nvidia-smi", "--query-compute-apps=pid,used_memory", "--format=csv,noheader,nounits")
	if err != nil {
		return nil
	}
	m := map[int]int{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(l, ",")
		if len(f) < 2 {
			continue
		}
		pid, e1 := strconv.Atoi(strings.TrimSpace(f[0]))
		mb, e2 := strconv.Atoi(strings.TrimSpace(f[1]))
		if e1 == nil && e2 == nil {
			m[pid] += mb
		}
	}
	return m
}
