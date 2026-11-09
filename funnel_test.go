package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testTSStatus = `{"BackendState":"Running","Self":{"HostName":"Box","DNSName":"box.tail1234.ts.net."}}`
	testTSServe  = `{"TCP":{"4096":{"TCPForward":"127.0.0.1:28894"},"443":{"HTTPS":true}},"Web":{"box.tail1234.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8766"}}}},"AllowFunnel":{"box.tail1234.ts.net:443":true}}`
)

func TestFunnelHost(t *testing.T) {
	if h, err := funnelHost([]byte(testTSStatus)); err != nil || h != "box.tail1234.ts.net" {
		t.Fatalf("host = %q, %v", h, err)
	}
	if _, err := funnelHost([]byte(`{"BackendState":"NeedsLogin","Self":{"DNSName":"box.tail1234.ts.net."}}`)); err == nil {
		t.Fatal("a signed-out Tailscale must be an error")
	}
	if _, err := funnelHost([]byte(`{"BackendState":"Running","Self":{"DNSName":""}}`)); err == nil {
		t.Fatal("no ts.net name must be an error")
	}
}

func TestFunnelTarget(t *testing.T) {
	if p, pub := funnelTarget([]byte(testTSServe), "box.tail1234.ts.net"); p != "http://127.0.0.1:8766" || !pub {
		t.Fatalf("target = %q public=%v", p, pub)
	}
	tailnetOnly := strings.Replace(testTSServe, `:443":true`, `:443":false`, 1)
	if p, pub := funnelTarget([]byte(tailnetOnly), "box.tail1234.ts.net"); p == "" || pub {
		t.Fatalf("tailnet-only serve read as public: %q %v", p, pub)
	}
	if p, pub := funnelTarget([]byte(`{}`), "box.tail1234.ts.net"); p != "" || pub {
		t.Fatalf("empty config = %q %v", p, pub)
	}
}

// fakeTS answers the tailscale CLI calls funnelEnsure makes and records them.
type fakeTS struct {
	mu    sync.Mutex
	serve string
	calls []string
}

func (f *fakeTS) run(_ time.Duration, _ string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := strings.Join(args, " ")
	f.calls = append(f.calls, line)
	switch {
	case strings.HasPrefix(line, "status"):
		return testTSStatus, nil
	case strings.HasPrefix(line, "serve status"):
		return f.serve, nil
	case strings.HasPrefix(line, "funnel --bg"):
		f.serve = testTSServe
		return "Available on the internet", nil
	case line == "funnel --https=443 off":
		f.serve = `{}`
		return "", nil
	}
	return "", errors.New("unexpected: " + line)
}

func (f *fakeTS) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func useFakeTS(t *testing.T, f *fakeTS) {
	old := tsRun
	tsRun = f.run
	t.Cleanup(func() { tsRun = old })
}

func TestFunnelEnsure(t *testing.T) {
	// Not set yet: it is turned on once, for the link port.
	f := &fakeTS{serve: `{}`}
	useFakeTS(t, f)
	u, err := funnelEnsure("tailscale", 8766)
	if err != nil || u != "https://box.tail1234.ts.net" {
		t.Fatalf("ensure = %q, %v", u, err)
	}
	if f.count("funnel --bg --https=443 http://127.0.0.1:8766") != 1 {
		t.Fatalf("funnel not set: %v", f.calls)
	}
	// Already set: nothing is changed.
	if u, err = funnelEnsure("tailscale", 8766); err != nil || u == "" || f.count("funnel --bg") != 1 {
		t.Fatalf("second ensure = %q, %v, calls %v", u, err, f.calls)
	}
	// https://host/ used by something else: left alone.
	other := &fakeTS{serve: strings.Replace(testTSServe, "8766", "3000", 1)}
	useFakeTS(t, other)
	if _, err = funnelEnsure("tailscale", 8766); err == nil || other.count("funnel") != 0 {
		t.Fatalf("someone else's funnel must not be replaced: %v, calls %v", err, other.calls)
	}
	funnelOff("tailscale", 8766)
	if other.count("funnel") != 0 {
		t.Fatalf("someone else's funnel must not be turned off: %v", other.calls)
	}
	// Ours is turned off.
	useFakeTS(t, f)
	funnelOff("tailscale", 8766)
	if f.count("funnel --https=443 off") != 1 {
		t.Fatalf("our funnel not turned off: %v", f.calls)
	}
}

func TestFunnelModeAndWanted(t *testing.T) {
	if !funnelMode(linkConfig{Tunnel: "Funnel"}) || funnelMode(linkConfig{Tunnel: "cloudflared"}) || funnelMode(linkConfig{}) {
		t.Fatal("funnelMode wrong")
	}
	if !tunnelWanted(linkConfig{Tunnel: "funnel"}) {
		t.Fatal("funnel mode must want a tunnel")
	}
	if tunnelKey(linkConfig{Port: 8766, Tunnel: "funnel"}) == tunnelKey(linkConfig{Port: 8766, Tunnel: "cloudflared"}) {
		t.Fatal("switching to the funnel must restart the tunnel")
	}
}

func TestSupervisorFunnel(t *testing.T) {
	var mu sync.Mutex
	var states []tunnelState
	var urls []string
	fail, offs, still := false, 0, true
	l := &fakeLauncher{}
	sup := &tunnelSupervisor{
		launch:  l.launch,
		exePath: func() string { return "tailscale" },
		ready:   func(ctx context.Context, port int) bool { return ctx.Err() == nil },
		save:    func(s tunnelState) { mu.Lock(); states = append(states, s); mu.Unlock() },
		backoff: time.Millisecond,
		onURL:   func(u string) { mu.Lock(); urls = append(urls, u); mu.Unlock() },
		funnel: func(exe string, port int) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return "", errors.New("Tailscale is not connected (Stopped)")
			}
			return "https://box.tail1234.ts.net", nil
		},
		funnelStop:  func(string, int) { mu.Lock(); offs++; mu.Unlock() },
		funnelStill: func() bool { mu.Lock(); defer mu.Unlock(); return still },
	}
	last := func() tunnelState {
		mu.Lock()
		defer mu.Unlock()
		if len(states) == 0 {
			return tunnelState{}
		}
		return states[len(states)-1]
	}
	c := linkConfig{Port: 8766, Tunnel: "funnel"}
	sup.start(c)
	waitFor(t, "funnel up", func() bool { s := last(); return s.State == "up" && s.URL == "https://box.tail1234.ts.net" })

	// Tailscale goes away: failed, URL withdrawn; back again: the same address.
	mu.Lock()
	fail = true
	mu.Unlock()
	waitFor(t, "failed", func() bool { return last().State == "failed" })
	mu.Lock()
	fail = false
	mu.Unlock()
	waitFor(t, "up again", func() bool { return last().State == "up" })

	// serve stopping: the funnel stays.
	sup.stop()
	mu.Lock()
	gotOffs, gotURLs := offs, strings.Join(urls, ",")
	mu.Unlock()
	if gotOffs != 0 {
		t.Fatal("the funnel must stay when serve stops")
	}
	if gotURLs != "https://box.tail1234.ts.net,,https://box.tail1234.ts.net," {
		t.Fatalf("urls published = %q", gotURLs)
	}
	if _, _, n, _ := l.snapshot(); n != 0 {
		t.Fatalf("funnel mode started cloudflared %d time(s)", n)
	}

	// Funnel mode switched off: the funnel is removed.
	sup.start(c)
	waitFor(t, "up", func() bool { return last().State == "up" })
	mu.Lock()
	still = false
	mu.Unlock()
	sup.stop()
	mu.Lock()
	defer mu.Unlock()
	if offs != 1 {
		t.Fatalf("funnel turned off %d time(s), want 1", offs)
	}
}

func TestConnectorReportPermanent(t *testing.T) {
	appHome = t.TempDir()
	tok := ensureConnectorToken()
	saveTunnelState(tunnelState{State: "up", URL: "https://box.tail1234.ts.net"})
	r := connectorReport()
	if !strings.Contains(r, "https://box.tail1234.ts.net/c/"+tok+"/mcp") {
		t.Fatalf("report lacks the permanent URL: %s", strings.ReplaceAll(r, tok, "***"))
	}
	if strings.Contains(r, "WARNING") || strings.Contains(r, "temporary") {
		t.Fatalf("a ts.net address must not get the temporary warning: %s", strings.ReplaceAll(r, tok, "***"))
	}
}

func TestClientIPFunnel(t *testing.T) {
	r := httptest.NewRequest("POST", "/mcp", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if ip := clientIP(r); ip != "203.0.113.9" {
		t.Fatalf("funnel caller = %q", ip)
	}
	// Only this machine's own proxy is believed.
	r.RemoteAddr = "192.168.1.50:50000"
	if ip := clientIP(r); ip != "192.168.1.50" {
		t.Fatalf("LAN caller with a made-up header = %q", ip)
	}
}
