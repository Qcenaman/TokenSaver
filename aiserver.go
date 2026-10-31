package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The local llama.cpp server is shared: remoteai and other apps may be using it too.
// TokenSaver only ever auto-stops a server it started itself, and only when no slot is
// busy, no TokenSaver job holds it, it isn't pinned, and it has been idle for
// ai_idle_minutes. A server that was already up is "external" and never auto-stopped.
// The state lives in data\ai_server.json so every TokenSaver process (MCP, serve, saved
// jobs) and the "ai-watch" watcher agree on it.

const (
	ownerTokenSaver = "tokensaver"
	ownerExternal   = "external"
	defaultAIIdle   = 15
	aiLeaseFresh    = 2 * time.Minute
	aiWatchEvery    = 20 * time.Second
)

type aiServerState struct {
	Owner       string    `json:"owner"`
	PID         int       `json:"pid,omitempty"`        // the server listening on the port
	ScriptPID   int       `json:"script_pid,omitempty"` // the console running the start script
	Port        int       `json:"port"`
	Base        string    `json:"base"`
	Model       string    `json:"model,omitempty"`
	Started     time.Time `json:"started"`
	LastActive  time.Time `json:"last_active"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	WatcherPID  int       `json:"watcher_pid,omitempty"`
	WatcherBeat time.Time `json:"watcher_beat,omitempty"`
}

// Swappable for tests.
var (
	aiNow          = time.Now
	aiPortPid      = func(port int) int { return listenerPid(port) }
	aiKill         = func(pid int) { killTree(pid) }
	aiStartWatcher = startAIWatcher
	aiStateMu      sync.Mutex
	aiHTTP         = &http.Client{Timeout: 3 * time.Second}
)

func aiStatePath() string { return filepath.Join(appHome, "ai_server.json") }
func aiLeaseDir() string  { return filepath.Join(appHome, "ai_leases") }

func loadAIState() (aiServerState, bool) {
	var st aiServerState
	b, err := os.ReadFile(aiStatePath())
	if err != nil || json.Unmarshal(b, &st) != nil {
		return aiServerState{}, false
	}
	return st, st.Owner != ""
}

func saveAIState(st aiServerState) { writeJSONAtomic(aiStatePath(), st) }
func clearAIState()                { os.Remove(aiStatePath()) }

func aiIdle() time.Duration {
	m := cfg.AIIdleMinutes
	if m <= 0 {
		m = defaultAIIdle
	}
	return time.Duration(m) * time.Minute
}

func aiPinned() bool { return cfg.AIKeepRunning }

// reloadAIConfig picks up keep_running / idle_minutes changed by another TokenSaver process.
func reloadAIConfig() {
	b, err := os.ReadFile(filepath.Join(appHome, "config.json"))
	if err != nil {
		return
	}
	var c struct {
		Keep bool `json:"ai_keep_running"`
		Idle int  `json:"ai_idle_minutes"`
	}
	if json.Unmarshal(b, &c) == nil {
		cfg.AIKeepRunning, cfg.AIIdleMinutes = c.Keep, c.Idle
	}
}

// setAIConfig saves the pin and the idle period into config.json, keeping its other keys.
func setAIConfig(keep *bool, idle *int) error {
	if idle != nil && (*idle < 1 || *idle > 24*60) {
		return errors.New("idle_minutes must be between 1 and 1440")
	}
	p := filepath.Join(appHome, "config.json")
	m := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &m)
	}
	if keep != nil {
		cfg.AIKeepRunning = *keep
		if *keep {
			m["ai_keep_running"] = true
		} else {
			delete(m, "ai_keep_running")
		}
	}
	if idle != nil {
		cfg.AIIdleMinutes = *idle
		m["ai_idle_minutes"] = *idle
	}
	return writeJSONAtomic(p, m)
}

// ---------- probing the server ----------

type aiProbe struct {
	Up, Busy, Known, Loading bool
	Fingerprint, Model       string
}

func aiGet(u string) (int, []byte, error) {
	resp, err := aiHTTP.Get(u)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b, nil
}

var metricRe = regexp.MustCompile(`(?m)^llamacpp:(requests_processing|requests_deferred|prompt_tokens_total|tokens_predicted_total)\s+([0-9.eE+-]+)`)

// probeAI asks llama.cpp whether it is up and whether anything is using it: GET /health,
// then /slots (is_processing per slot) and /metrics (requests_processing). The fingerprint
// changes whenever a request was handled, so short requests between two checks still count.
func probeAI(base string) aiProbe {
	var p aiProbe
	code, _, err := aiGet(base + "/health")
	if err != nil {
		return p
	}
	if code == 503 { // still loading the model
		p.Up, p.Loading, p.Busy, p.Known = true, true, true, true
		return p
	}
	ms, merr := (&aiClient{kind: "openai", base: base, hc: aiHTTP}).models()
	if code != 200 && merr != nil {
		return p
	}
	p.Up = true
	if merr == nil && len(ms) > 0 {
		p.Model = ms[0]
	}
	var fp []string
	if code, b, err := aiGet(base + "/slots"); err == nil && code == 200 {
		var slots []map[string]any
		if json.Unmarshal(b, &slots) == nil {
			p.Known = true
			for _, s := range slots {
				if v, _ := s["is_processing"].(bool); v {
					p.Busy = true
				}
				if v, ok := s["state"].(float64); ok && v != 0 { // older llama.cpp
					p.Busy = true
				}
			}
			h := sha1.Sum(b)
			fp = append(fp, "slots:"+hex.EncodeToString(h[:8]))
		}
	}
	if code, b, err := aiGet(base + "/metrics"); err == nil && code == 200 {
		vals := map[string]float64{}
		for _, m := range metricRe.FindAllStringSubmatch(string(b), -1) {
			v, _ := strconv.ParseFloat(m[2], 64)
			vals[m[1]] = v
		}
		if len(vals) > 0 {
			p.Known = true
			if vals["requests_processing"] > 0 || vals["requests_deferred"] > 0 {
				p.Busy = true
			}
			fp = append(fp, fmt.Sprintf("metrics:%.0f/%.0f", vals["prompt_tokens_total"], vals["tokens_predicted_total"]))
		}
	}
	p.Fingerprint = strings.Join(fp, " ")
	return p
}

// ---------- ownership ----------

// aiOwnerOf says who runs the server on port now: TokenSaver only while the state says so
// and the listening process is still the one it started.
func aiOwnerOf(st aiServerState, port int) string {
	if st.Owner != ownerTokenSaver || (st.Port != 0 && st.Port != port) {
		return ownerExternal
	}
	if st.PID > 0 && aiPortPid(port) != st.PID {
		return ownerExternal
	}
	return ownerTokenSaver
}

// aiAdopt is called when the server was already up before a job: it stays TokenSaver's
// if TokenSaver started it earlier, otherwise it is recorded as external.
func aiAdopt(base string, port int) string {
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	now := aiNow()
	if st, ok := loadAIState(); ok && aiOwnerOf(st, port) == ownerTokenSaver {
		st.LastActive = now
		saveAIState(st)
		go aiEnsureWatcher()
		return ownerTokenSaver
	}
	saveAIState(aiServerState{Owner: ownerExternal, PID: aiPortPid(port), Port: port, Base: base, Started: now, LastActive: now})
	return ownerExternal
}

// aiClaim records a server TokenSaver just started and makes sure a watcher looks after it.
func aiClaim(base string, port, scriptPid, pid int, model string) {
	aiStateMu.Lock()
	now := aiNow()
	saveAIState(aiServerState{Owner: ownerTokenSaver, PID: pid, ScriptPID: scriptPid, Port: port, Base: base, Model: model, Started: now, LastActive: now})
	aiStateMu.Unlock()
	aiEnsureWatcher()
}

// aiTouch restarts the idle timer.
func aiTouch() {
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	if st, ok := loadAIState(); ok {
		st.LastActive = aiNow()
		saveAIState(st)
	}
}

// aiLease marks the server as in use by a TokenSaver job until the returned release is
// called (refreshed every 30 s, so a crashed job's lease goes stale on its own).
func aiLease(id string) func() {
	os.MkdirAll(aiLeaseDir(), 0o755)
	if id == "" {
		id = fmt.Sprintf("p%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	p := filepath.Join(aiLeaseDir(), id)
	os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o644)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				n := time.Now()
				os.Chtimes(p, n, n)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			os.Remove(p)
			aiTouch()
		})
	}
}

func aiLeasesActive() bool {
	es, _ := os.ReadDir(aiLeaseDir())
	active := false
	for _, e := range es {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if age := time.Since(fi.ModTime()); age < aiLeaseFresh {
			active = true
		} else if age > time.Hour {
			os.Remove(filepath.Join(aiLeaseDir(), e.Name()))
		}
	}
	return active
}

// ---------- deciding and stopping ----------

// shouldAutoStop is the one rule for automatic shutdown: owned by TokenSaver, not pinned,
// visibly idle (no busy slot, no TokenSaver job) and idle for the whole idle period.
func shouldAutoStop(st aiServerState, pr aiProbe, now time.Time) (bool, string) {
	switch {
	case st.Owner != ownerTokenSaver:
		return false, "external server: never stopped by TokenSaver"
	case aiPinned():
		return false, "pinned (keep running)"
	case !pr.Known:
		return false, "can't see whether it is in use (no /slots or /metrics)"
	case pr.Busy:
		return false, "busy"
	case aiLeasesActive():
		return false, "a TokenSaver job is using it"
	}
	if left := aiIdle() - now.Sub(st.LastActive); left > 0 {
		return false, fmt.Sprintf("idle, stops in %d min", int((left+time.Minute-1)/time.Minute))
	}
	return true, "idle for " + fmt.Sprint(int(aiIdle()/time.Minute)) + " min"
}

func aiStopOwned(st aiServerState) {
	if st.ScriptPID > 0 {
		aiKill(st.ScriptPID)
	}
	if st.PID > 0 && aiPortPid(st.Port) == st.PID {
		aiKill(st.PID)
	}
}

// aiWatchTick checks the shared server once. It returns false when there is nothing left
// to watch (stopped, gone, or not TokenSaver's).
func aiWatchTick() (bool, string) {
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	reloadAIConfig()
	st, ok := loadAIState()
	if !ok || st.Owner != ownerTokenSaver {
		return false, "not started by TokenSaver"
	}
	pr := probeAI(st.Base)
	pid := aiPortPid(st.Port)
	if !pr.Up {
		if pid == 0 {
			clearAIState()
			return false, "server is gone"
		}
		return true, "not answering"
	}
	if st.PID > 0 && pid > 0 && pid != st.PID {
		clearAIState()
		return false, "another server is on the port now"
	}
	if st.Model != "" && pr.Model != "" && pr.Model != st.Model {
		clearAIState()
		return false, "another model was loaded"
	}
	if st.PID == 0 {
		st.PID = pid
	}
	now := aiNow()
	if pr.Busy || pr.Fingerprint != st.Fingerprint {
		st.LastActive = now
	}
	st.Fingerprint = pr.Fingerprint
	stop, why := shouldAutoStop(st, pr, now)
	if !stop {
		saveAIState(st)
		return true, why
	}
	// One last look right before stopping, in case a request just came in.
	if again := probeAI(st.Base); !again.Up || again.Busy || again.Fingerprint != pr.Fingerprint {
		st.LastActive, st.Fingerprint = aiNow(), again.Fingerprint
		saveAIState(st)
		return true, "in use again"
	}
	aiStopOwned(st)
	clearAIState()
	return false, "stopped: " + why
}

// aiEnsureWatcher starts the watcher when a TokenSaver-owned server has none alive.
func aiEnsureWatcher() {
	st, ok := loadAIState()
	if !ok || st.Owner != ownerTokenSaver {
		return
	}
	if st.WatcherPID > 0 && aiNow().Sub(st.WatcherBeat) < 3*aiWatchEvery && pidAlive(st.WatcherPID) {
		return
	}
	aiStartWatcher()
}

// aiWatchMain is "tokensaver ai-watch": it looks after a TokenSaver-started server until
// it is stopped or gone. Only one watcher runs; a second one exits by itself.
func aiWatchMain() int {
	me := os.Getpid()
	for {
		aiStateMu.Lock()
		st, ok := loadAIState()
		if !ok || st.Owner != ownerTokenSaver {
			aiStateMu.Unlock()
			return 0
		}
		if st.WatcherPID > 0 && st.WatcherPID != me && pidAlive(st.WatcherPID) && aiNow().Sub(st.WatcherBeat) < 3*aiWatchEvery {
			aiStateMu.Unlock()
			return 0
		}
		st.WatcherPID, st.WatcherBeat = me, aiNow()
		saveAIState(st)
		aiStateMu.Unlock()
		if keep, _ := aiWatchTick(); !keep {
			return 0
		}
		time.Sleep(aiWatchEvery)
	}
}

// ---------- explicit stop and status ----------

func aiTarget() (string, int, bool) {
	if cfg.AIEndpoint == "" && cfg.AIScript != "" {
		host, port := scriptAddr(cfg.AIScript)
		return fmt.Sprintf("http://%s:%d", host, port), port, true
	}
	return "", 0, false
}

// aiSafeStop is the manual stop (local_ai stop, job cancel local_ai). It refuses with a
// warning while the server is busy, can't be checked, is used by a TokenSaver job or
// wasn't started by TokenSaver, unless confirmed. The pin only blocks automatic stops.
func aiSafeStop(confirmed bool) string {
	base, port, ok := aiTarget()
	if !ok {
		return "No llama.cpp start script is set up (ai_script), so there is no local AI server to stop."
	}
	aiStateMu.Lock()
	defer aiStateMu.Unlock()
	pr := probeAI(base)
	pid := aiPortPid(port)
	if !pr.Up && pid == 0 {
		clearAIState()
		return "The local AI server isn't running."
	}
	st, _ := loadAIState()
	owner := aiOwnerOf(st, port)
	if !confirmed {
		var why []string
		if pr.Busy {
			why = append(why, "it is busy right now (a request is being processed, maybe by remoteai or another app)")
		} else if pr.Up && !pr.Known {
			why = append(why, "it can't tell whether the server is in use (no /slots or /metrics answer)")
		}
		if aiLeasesActive() {
			why = append(why, "a TokenSaver job is using it")
		}
		if owner == ownerExternal {
			why = append(why, "it wasn't started by TokenSaver")
		}
		if len(why) > 0 {
			return "WARNING: the local AI server was NOT stopped: " + strings.Join(why, "; ") + ". Tell the user; only if they say yes, repeat with confirmed:true."
		}
	}
	if owner == ownerTokenSaver && st.ScriptPID > 0 {
		aiKill(st.ScriptPID)
	}
	if pid > 0 {
		aiKill(pid)
	}
	clearAIState()
	return fmt.Sprintf("Stopped the local AI server on port %d (owner: %s).", port, owner)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// aiServerStatus is the models line: running, owner, pinned, busy, idle minutes left.
func aiServerStatus(base string, port int) string {
	reloadAIConfig()
	pinned := yesNo(aiPinned())
	idleMin := int(aiIdle() / time.Minute)
	pr := probeAI(base)
	if !pr.Up {
		return fmt.Sprintf("not running | pinned: %s | idle timeout: %d min (starts when a job needs it)", pinned, idleMin)
	}
	st, _ := loadAIState()
	owner := aiOwnerOf(st, port)
	busy := yesNo(pr.Busy)
	switch {
	case pr.Loading:
		busy = "loading"
	case !pr.Known:
		busy = "unknown (no /slots or /metrics)"
	case !pr.Busy && aiLeasesActive():
		busy = "yes (TokenSaver job)"
	}
	var idle string
	switch {
	case owner == ownerExternal:
		idle = "never auto-stopped (external)"
	case aiPinned():
		idle = "pinned, not auto-stopped"
	case busy != "no":
		idle = fmt.Sprintf("in use (%d min timer restarts when it goes idle)", idleMin)
	default:
		left := aiIdle() - aiNow().Sub(st.LastActive)
		idle = fmt.Sprintf("%d min left of %d", max(0, int((left+time.Minute-1)/time.Minute)), idleMin)
	}
	model := pr.Model
	if model == "" {
		model = "?"
	}
	return fmt.Sprintf("running: %s | owner: %s | pinned: %s | busy: %s | idle: %s", model, owner, pinned, busy, idle)
}
