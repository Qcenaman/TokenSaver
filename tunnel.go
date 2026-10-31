package main

// tunnel: the cloudflared child of "tokensaver serve". Exactly one runs at a time:
//   - the supervisor's loop is sequential: a process is waited for before the next one starts,
//     and stop() returns only once the current process has exited;
//   - on Windows each cloudflared is put in a Job Object with KILL_ON_JOB_CLOSE, so it dies
//     with serve (crash, kill, update);
//   - before each launch, any cloudflared left over for our port (an orphan of an older serve)
//     is killed;
//   - cloudflared starts only once the link port answers /health.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// tunnelProc is a started cloudflared (or a fake one in tests).
type tunnelProc interface {
	Pid() int
	Wait() error
	Kill() error
}

// tunnelLauncher starts cloudflared with its stderr going to w.
type tunnelLauncher func(exe string, args []string, w io.Writer) (tunnelProc, error)

type tunnelSupervisor struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	onURL  func(string) // called when the advertised URL changes (new URL or "")

	// Swappable for tests; nil = the real thing.
	launch  tunnelLauncher
	exePath func() string
	ready   func(ctx context.Context, port int) bool
	save    func(tunnelState)
	backoff time.Duration
}

func (t *tunnelSupervisor) saveState(s tunnelState) {
	if t.save != nil {
		t.save(s)
		return
	}
	saveTunnelState(s)
}

func (t *tunnelSupervisor) running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done != nil
}

func (t *tunnelSupervisor) start(c linkConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done != nil {
		return
	}
	exe := cloudflaredExe
	if t.exePath != nil {
		exe = t.exePath
	}
	p := exe()
	if p == "" {
		t.saveState(tunnelState{State: "missing", Error: "cloudflared not installed: " + cloudflaredInstall})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.cancel, t.done = cancel, done
	go func() {
		defer close(done)
		t.loop(ctx, p, c)
	}()
}

// stop ends the tunnel and returns only after cloudflared has exited.
func (t *tunnelSupervisor) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done == nil {
		return
	}
	t.cancel()
	<-t.done
	t.cancel, t.done = nil, nil
	t.saveState(tunnelState{State: "off"})
	if t.onURL != nil {
		t.onURL("")
	}
}

// restart stops the old cloudflared (waiting for it to exit) before starting a new one.
func (t *tunnelSupervisor) restart(c linkConfig) {
	t.stop()
	t.start(c)
}

func (t *tunnelSupervisor) loop(ctx context.Context, exe string, c linkConfig) {
	ready := waitLinkReady
	if t.ready != nil {
		ready = t.ready
	}
	base := t.backoff
	if base == 0 {
		base = 2 * time.Second
	}
	backoff := base
	for ctx.Err() == nil {
		t.saveState(tunnelState{State: "waiting", Error: fmt.Sprintf("waiting for port %d to listen", c.Port)})
		if !ready(ctx, c.Port) {
			return
		}
		started := time.Now()
		err := t.runOnce(ctx, exe, c)
		if ctx.Err() != nil {
			return
		}
		msg := "cloudflared exited"
		if err != nil {
			msg = err.Error()
		}
		serveLog("tunnel: %s", msg)
		t.saveState(tunnelState{State: "failed", Error: msg})
		if t.onURL != nil {
			t.onURL("")
		}
		if time.Since(started) > time.Minute {
			backoff = base
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}

func tunnelArgs(c linkConfig) (args []string, named bool) {
	local := fmt.Sprintf("http://127.0.0.1:%d", c.Port)
	named = c.TunnelHostname != "" && c.TunnelCreds != "" && exists(c.TunnelCreds)
	if named {
		return []string{"tunnel", "--no-autoupdate", "--credentials-file", c.TunnelCreds, "--url", local, "run", tunnelIDFromCreds(c.TunnelCreds)}, true
	}
	return []string{"tunnel", "--no-autoupdate", "--url", local}, false
}

// runOnce runs one cloudflared until it exits or ctx ends; either way the process has exited
// when it returns. The URL is published as soon as it is known.
func (t *tunnelSupervisor) runOnce(ctx context.Context, exe string, c linkConfig) error {
	launch := t.launch
	if launch == nil {
		launch = launchCloudflared
	}
	args, named := tunnelArgs(c)
	pr, pw := io.Pipe()
	p, err := launch(exe, args, pw)
	if err != nil {
		pw.Close()
		return err
	}
	pid := p.Pid()
	serveLog("tunnel: cloudflared started, pid %d", pid)
	t.saveState(tunnelState{State: "starting", PID: pid})
	url := ""
	publish := func(u string) {
		url = u
		t.saveState(tunnelState{State: "up", URL: u, PID: pid})
		if t.onURL != nil {
			t.onURL(u)
		}
	}
	if named {
		publish("https://" + c.TunnelHostname)
	}
	go func() {
		buf := make([]byte, 4096)
		var line []byte
		for {
			n, err := pr.Read(buf)
			for _, b := range buf[:n] {
				if b == '\n' {
					if !named {
						if u := parseCloudflaredURL(string(line)); u != "" && u != url {
							publish(u)
						}
					}
					line = line[:0]
				} else if len(line) < 4096 {
					line = append(line, b)
				}
			}
			if err != nil {
				return
			}
		}
	}()
	waitCh := make(chan error, 1)
	go func() {
		err := p.Wait()
		pw.Close()
		waitCh <- err
	}()
	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		p.Kill()
		<-waitCh
		serveLog("tunnel: cloudflared pid %d stopped", pid)
		return ctx.Err()
	}
}

// waitLinkReady waits until the link server answers /health on 127.0.0.1:port (false = ctx ended).
func waitLinkReady(ctx context.Context, port int) bool {
	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	cl := &http.Client{Timeout: 2 * time.Second}
	start, logged := time.Now(), false
	for {
		if resp, err := cl.Get(url); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return true
			}
		}
		if !logged && time.Since(start) > 15*time.Second {
			serveLog("tunnel: port %d still not answering; cloudflared waits for it", port)
			logged = true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// --- orphans --------------------------------------------------------------------------------

// tunnelOrphans picks the cloudflared processes whose command line targets our link port
// (http://127.0.0.1:<port> or localhost:<port>), except the pids in keep.
func tunnelOrphans(ps []procInfo, port int, keep map[int]bool) []int {
	re := regexp.MustCompile(fmt.Sprintf(`(?i)//(?:127\.0\.0\.1|localhost):%d(?:[^0-9]|$)`, port))
	var out []int
	for _, p := range ps {
		name := strings.ToLower(filepath.Base(strings.ReplaceAll(p.name, `\`, "/")))
		if name != "cloudflared.exe" && name != "cloudflared" {
			continue
		}
		if keep[p.pid] || !re.MatchString(p.cmd) {
			continue
		}
		out = append(out, p.pid)
	}
	return out
}

// killTunnelOrphans kills leftover cloudflared processes for our port; returns how many.
func killTunnelOrphans(port int) int {
	ps, err := procs()
	if err != nil {
		serveLog("tunnel: couldn't list processes for orphan cleanup: %v", err)
		return 0
	}
	pids := tunnelOrphans(ps, port, nil)
	for _, pid := range pids {
		killTree(pid)
		serveLog("tunnel: killed leftover cloudflared pid %d", pid)
	}
	return len(pids)
}

// --- real launcher --------------------------------------------------------------------------

type execProc struct{ cmd *exec.Cmd }

func (p execProc) Pid() int    { return p.cmd.Process.Pid }
func (p execProc) Wait() error { return p.cmd.Wait() }
func (p execProc) Kill() error { return p.cmd.Process.Kill() }

func launchCloudflared(exe string, args []string, w io.Writer) (tunnelProc, error) {
	killTunnelOrphans(loadLinkCfg().Port)
	cmd := exec.Command(exe, args...)
	hideWindow(cmd)
	prepareChildInJob(cmd)
	cmd.Stdout = io.Discard
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := putChildInJob(cmd.Process.Pid); err != nil {
		serveLog("tunnel: job object: %v", err)
	}
	return execProc{cmd}, nil
}
