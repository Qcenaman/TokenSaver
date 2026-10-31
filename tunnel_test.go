package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTunnelOrphansOnlyOurPort(t *testing.T) {
	ps := []procInfo{
		{pid: 1, name: "cloudflared.exe", cmd: `"C:\Program Files (x86)\cloudflared\cloudflared.exe" tunnel --no-autoupdate --url http://127.0.0.1:8766`},
		{pid: 2, name: "cloudflared.exe", cmd: `cloudflared.exe tunnel --url http://127.0.0.1:87660`},
		{pid: 3, name: "CLOUDFLARED.EXE", cmd: `cloudflared tunnel --url http://localhost:8766/ --no-autoupdate`},
		{pid: 4, name: "cloudflared.exe", cmd: `cloudflared tunnel --url http://127.0.0.1:9000`},
		{pid: 5, name: "tokensaver.exe", cmd: `tokensaver.exe serve --url http://127.0.0.1:8766`},
		{pid: 6, name: "cloudflared.exe", cmd: `cloudflared tunnel run mytunnel`},
		{pid: 7, name: "cloudflared.exe", cmd: `cloudflared tunnel --url http://127.0.0.1:8766`},
		{pid: 8, name: "cloudflared", cmd: `/usr/bin/cloudflared tunnel --url http://127.0.0.1:8766`},
	}
	got := tunnelOrphans(ps, 8766, map[int]bool{7: true})
	want := []int{1, 3, 8}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("orphans = %v, want %v", got, want)
	}
}

// fakeLauncher counts live fake cloudflared processes and remembers the most ever alive at once.
type fakeLauncher struct {
	mu       sync.Mutex
	live     int
	max      int
	launched int
	procs    []*fakeProc
}

type fakeProc struct {
	pid  int
	done chan struct{}
	once sync.Once
	l    *fakeLauncher
}

func (p *fakeProc) Pid() int    { return p.pid }
func (p *fakeProc) Wait() error { <-p.done; return nil }
func (p *fakeProc) Kill() error { p.exit(); return nil }
func (p *fakeProc) exit() {
	p.once.Do(func() {
		p.l.mu.Lock()
		p.l.live--
		p.l.mu.Unlock()
		close(p.done)
	})
}

func (l *fakeLauncher) launch(exe string, args []string, w io.Writer) (tunnelProc, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.launched++
	l.live++
	l.max = max(l.max, l.live)
	p := &fakeProc{pid: 1000 + l.launched, done: make(chan struct{}), l: l}
	l.procs = append(l.procs, p)
	go fmt.Fprintf(w, "INF |  https://fake-%d.trycloudflare.com  |\n", p.pid)
	return p, nil
}

func (l *fakeLauncher) snapshot() (live, max, launched int, last *fakeProc) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.procs) > 0 {
		last = l.procs[len(l.procs)-1]
	}
	return l.live, l.max, l.launched, last
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newTestSupervisor(l *fakeLauncher) *tunnelSupervisor {
	return &tunnelSupervisor{
		launch:  l.launch,
		exePath: func() string { return "cloudflared" },
		ready:   func(ctx context.Context, port int) bool { return ctx.Err() == nil },
		save:    func(tunnelState) {},
		backoff: time.Millisecond,
	}
}

func TestSupervisorNeverTwoCloudflared(t *testing.T) {
	l := &fakeLauncher{}
	sup := newTestSupervisor(l)
	c := linkConfig{Port: 8766}
	sup.start(c)
	sup.start(c) // already running: no second process
	waitFor(t, "first launch", func() bool { _, _, n, _ := l.snapshot(); return n == 1 })

	// cloudflared dies by itself: the supervisor starts exactly one replacement.
	for i := 0; i < 5; i++ {
		_, _, n, last := l.snapshot()
		last.exit()
		waitFor(t, "relaunch after exit", func() bool { _, _, m, _ := l.snapshot(); return m == n+1 })
	}
	// Restarts stop the old process before the new one starts.
	for i := 0; i < 10; i++ {
		sup.restart(c)
		if live, _, _, _ := l.snapshot(); live > 1 {
			t.Fatalf("%d cloudflared alive after restart", live)
		}
	}
	waitFor(t, "running after restarts", func() bool { live, _, _, _ := l.snapshot(); return live == 1 })
	sup.stop()
	live, maxLive, _, _ := l.snapshot()
	if live != 0 {
		t.Fatalf("%d cloudflared still alive after stop", live)
	}
	if maxLive != 1 {
		t.Fatalf("at most 1 cloudflared may run, saw %d at once", maxLive)
	}
}

func TestTunnelWaitsForListeningPort(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // the port is free: nothing listens yet

	l := &fakeLauncher{}
	sup := newTestSupervisor(l)
	sup.ready = nil // the real check
	sup.start(linkConfig{Port: port})
	defer sup.stop()

	time.Sleep(1500 * time.Millisecond)
	if _, _, n, _ := l.snapshot(); n != 0 {
		t.Fatal("cloudflared started before the link port was listening")
	}
	ln, err = net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Skip("port taken meanwhile:", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })}
	go srv.Serve(ln)
	defer srv.Close()
	waitFor(t, "launch once the port listens", func() bool { _, _, n, _ := l.snapshot(); return n == 1 })
}

func TestPlanLinkInstall(t *testing.T) {
	exe := `D:\Claude Apps\TokenSaver\app\tokensaver.exe`
	cf := `C:\Program Files (x86)\cloudflared\cloudflared.exe`
	p := planLinkInstall(exe, cf, 8766)
	if p.Account != "LocalSystem" || p.StartType != "auto" || p.ServiceName != linkServiceName {
		t.Fatalf("service must start at boot as LocalSystem: %+v", p)
	}
	if strings.Join(p.Args, " ") != "serve" || p.Exe != exe {
		t.Fatalf("service must run %q serve, got %q %v", exe, p.Exe, p.Args)
	}
	if len(p.RemoveTasks) != 1 || p.RemoveTasks[0] != "TokenSaver Link" {
		t.Fatalf("the old sign-in task must be removed: %v", p.RemoveTasks)
	}
	lastDelete, firstAdd := -1, len(p.Firewall)
	adds := map[string]int{}
	for i, l := range p.Firewall {
		switch {
		case strings.Contains(l, " delete rule "):
			lastDelete = i
		case strings.Contains(l, " add rule "):
			firstAdd = min(firstAdd, i)
			adds[l]++
		}
	}
	if lastDelete > firstAdd {
		t.Fatal("all deletes (duplicate cleanup) must come before the adds")
	}
	for _, need := range []string{
		`delete rule name=all program="` + exe + `"`,
		`delete rule name=all program="` + cf + `"`,
		`add rule name="TokenSaver Link" dir=in action=allow protocol=TCP localport=8766`,
		`add rule name="TokenSaver app" dir=in action=allow program="` + exe + `"`,
		`add rule name="TokenSaver app" dir=out action=allow program="` + exe + `"`,
		`add rule name="TokenSaver cloudflared" dir=in action=allow program="` + cf + `"`,
		`add rule name="TokenSaver cloudflared" dir=out action=allow program="` + cf + `"`,
	} {
		found := false
		for _, l := range p.Firewall {
			if strings.Contains(l, need) {
				found = true
			}
		}
		if !found {
			t.Errorf("firewall plan lacks %q", need)
		}
	}
	for l, n := range adds {
		if n > 1 {
			t.Errorf("rule added %d times: %s", n, l)
		}
		if !strings.Contains(l, "profile=any") {
			t.Errorf("rule must cover every profile: %s", l)
		}
	}
}

func TestIsServeCommandLine(t *testing.T) {
	yes := []string{`"D:\Claude Apps\TokenSaver\app\tokensaver.exe" serve`, `tokensaver serve`, `C:\x\TokenSaver.exe  serve `}
	no := []string{`"D:\Claude Apps\TokenSaver\app\tokensaver.exe" mcp`, `tokensaver.exe helper`, `cloudflared serve`, `serve`}
	for _, c := range yes {
		if !isServeCommandLine(c) {
			t.Errorf("expected serve: %s", c)
		}
	}
	for _, c := range no {
		if isServeCommandLine(c) {
			t.Errorf("not a serve: %s", c)
		}
	}
}

func TestExeWatchNeedsStableChange(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokensaver.exe")
	os.WriteFile(p, []byte("v1"), 0o644)
	w := &exeWatch{path: p}
	fi, _ := os.Stat(p)
	w.size, w.mod = fi.Size(), fi.ModTime()
	if w.changed() {
		t.Fatal("unchanged file reported as changed")
	}
	os.WriteFile(p, []byte("version two"), 0o644)
	if w.changed() {
		t.Fatal("a change must be seen twice (copy finished) before it counts")
	}
	if !w.changed() {
		t.Fatal("a stable change must be reported")
	}
}
