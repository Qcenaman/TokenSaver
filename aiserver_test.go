package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const fakeServerPID = 4242

// fakeLlama mocks llama.cpp's /health, /v1/models and /slots.
type fakeLlama struct {
	busy  atomic.Bool
	tasks atomic.Int64
}

type aiTestEnv struct {
	f      *fakeLlama
	base   string
	port   int
	killed []int
}

func setupAIServer(t *testing.T) *aiTestEnv {
	t.Helper()
	oldHome, oldCfg := appHome, cfg
	oldKill, oldPid, oldWatch := aiKill, aiPortPid, aiStartWatcher
	t.Cleanup(func() {
		appHome, cfg = oldHome, oldCfg
		aiKill, aiPortPid, aiStartWatcher = oldKill, oldPid, oldWatch
	})
	appHome = t.TempDir()
	cfg = Config{Workers: 4}

	e := &aiTestEnv{f: &fakeLlama{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"status":"ok"}`)) })
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"object":"list","data":[{"id":"qwen3.6-27b"}],"models":[{"name":"qwen3.6-27b","model":"qwen3.6-27b"}]}`))
	})
	mux.HandleFunc("/slots", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `[{"id":0,"id_task":%d,"is_processing":%v}]`, e.f.tasks.Load(), e.f.busy.Load())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	e.port, _ = strconv.Atoi(u.Port())
	e.base = fmt.Sprintf("http://127.0.0.1:%d", e.port)

	script := filepath.Join(appHome, "start-qwen.bat")
	os.WriteFile(script, []byte(fmt.Sprintf("llama-server.exe --host 127.0.0.1 --port %d\r\n", e.port)), 0o644)
	cfg.AIScript = script

	aiKill = func(pid int) { e.killed = append(e.killed, pid) }
	aiPortPid = func(int) int { return fakeServerPID }
	aiStartWatcher = func() {}
	return e
}

// owned writes the state of a server TokenSaver started, last used `ago`, with the
// current fingerprint so the check sees no new activity.
func (e *aiTestEnv) owned(ago time.Duration) {
	saveAIState(aiServerState{
		Owner: ownerTokenSaver, PID: fakeServerPID, Port: e.port, Base: e.base, Model: "qwen3.6-27b",
		Started: time.Now().Add(-time.Hour), LastActive: time.Now().Add(-ago), Fingerprint: probeAI(e.base).Fingerprint,
	})
}

func TestExternalServerNeverStopped(t *testing.T) {
	e := setupAIServer(t)
	if got := aiAdopt(e.base, e.port); got != ownerExternal {
		t.Fatalf("a server that was already up should be external, got %q", got)
	}
	st, _ := loadAIState()
	st.LastActive = time.Now().Add(-24 * time.Hour)
	saveAIState(st)

	if keep, why := aiWatchTick(); keep {
		t.Fatalf("watcher should leave an external server alone, kept watching: %s", why)
	}
	if stop, _ := shouldAutoStop(st, probeAI(e.base), time.Now()); stop {
		t.Fatal("shouldAutoStop said yes for an external server")
	}
	if msg := aiSafeStop(false); !strings.Contains(msg, "NOT stopped") {
		t.Fatalf("manual stop of an external server needs confirmation, got: %s", msg)
	}
	if len(e.killed) != 0 {
		t.Fatalf("external server was killed: %v", e.killed)
	}
	if s := aiServerStatus(e.base, e.port); !strings.Contains(s, "owner: external") || !strings.Contains(s, "never auto-stopped") {
		t.Fatalf("status: %s", s)
	}
}

func TestBusyServerNotStopped(t *testing.T) {
	e := setupAIServer(t)
	e.owned(time.Hour)
	e.f.busy.Store(true)

	if keep, why := aiWatchTick(); !keep || len(e.killed) != 0 {
		t.Fatalf("busy server must not be stopped (keep=%v, %s, killed %v)", keep, why, e.killed)
	}
	if st, _ := loadAIState(); time.Since(st.LastActive) > time.Minute {
		t.Fatal("a busy slot should restart the idle timer")
	}
	if msg := aiSafeStop(false); !strings.Contains(msg, "busy") || len(e.killed) != 0 {
		t.Fatalf("manual stop of a busy server should be refused: %s (killed %v)", msg, e.killed)
	}
	if s := aiServerStatus(e.base, e.port); !strings.Contains(s, "busy: yes") || !strings.Contains(s, "owner: tokensaver") {
		t.Fatalf("status: %s", s)
	}
	if msg := aiSafeStop(true); !strings.Contains(msg, "Stopped") || len(e.killed) != 1 {
		t.Fatalf("confirmed stop should go ahead: %s (killed %v)", msg, e.killed)
	}
}

func TestIdleTimeoutStopsOnlyOwnedIdleServer(t *testing.T) {
	e := setupAIServer(t)

	// Idle, but not long enough yet.
	e.owned(5 * time.Minute)
	if keep, _ := aiWatchTick(); !keep || len(e.killed) != 0 {
		t.Fatalf("stopped before the idle period ended (killed %v)", e.killed)
	}
	if s := aiServerStatus(e.base, e.port); !strings.Contains(s, "10 min left of 15") {
		t.Fatalf("status should show minutes left: %s", s)
	}

	// Long idle, but a request was handled since the last check: timer restarts.
	e.owned(20 * time.Minute)
	e.f.tasks.Add(1)
	if keep, _ := aiWatchTick(); !keep || len(e.killed) != 0 {
		t.Fatalf("a request since the last check should restart the timer (killed %v)", e.killed)
	}

	// Long idle and nothing happened: owned server is stopped and forgotten.
	e.owned(20 * time.Minute)
	if keep, why := aiWatchTick(); keep || len(e.killed) != 1 || e.killed[0] != fakeServerPID {
		t.Fatalf("owned idle server should be stopped (keep=%v, %s, killed %v)", keep, why, e.killed)
	}
	if _, ok := loadAIState(); ok {
		t.Fatal("state should be cleared after stopping")
	}

	// A TokenSaver job holding the server also counts as use.
	e.killed = nil
	e.owned(20 * time.Minute)
	release := aiLease("job-1")
	if keep, _ := aiWatchTick(); !keep || len(e.killed) != 0 {
		t.Fatalf("server in use by a TokenSaver job was stopped (killed %v)", e.killed)
	}
	release()
}

func TestPinBlocksAutoStop(t *testing.T) {
	e := setupAIServer(t)
	yes := true
	if err := setAIConfig(&yes, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(appHome, "config.json"))
	if !strings.Contains(string(b), `"ai_keep_running": true`) && !strings.Contains(string(b), `"ai_keep_running":true`) {
		t.Fatalf("pin not saved: %s", b)
	}
	e.owned(2 * time.Hour)
	if keep, why := aiWatchTick(); !keep || len(e.killed) != 0 {
		t.Fatalf("pinned server was stopped (keep=%v, %s, killed %v)", keep, why, e.killed)
	}
	if s := aiServerStatus(e.base, e.port); !strings.Contains(s, "pinned: yes") {
		t.Fatalf("status: %s", s)
	}

	no := false
	if err := setAIConfig(&no, nil); err != nil {
		t.Fatal(err)
	}
	e.owned(2 * time.Hour)
	if keep, _ := aiWatchTick(); keep || len(e.killed) != 1 {
		t.Fatalf("unpinned idle server should be stopped (killed %v)", e.killed)
	}
}
