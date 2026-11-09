package main

// funnel: a permanent public address for "tokensaver serve" through Tailscale Funnel
// (https://<this-computer>.<tailnet>.ts.net) instead of a cloudflared tunnel. link.json
// "tunnel": "funnel" turns it on. tailscaled keeps the funnel across restarts and reboots and the
// name never changes, so serve only makes sure it is set and points at the link port.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// funnelMode says whether link.json asks for the Tailscale Funnel instead of cloudflared.
func funnelMode(c linkConfig) bool {
	switch strings.ToLower(c.Tunnel) {
	case "funnel", "tailscale", "permanent":
		return true
	}
	return false
}

// funnelHost reads this machine's ts.net name from "tailscale status --json".
func funnelHost(statusJSON []byte) (string, error) {
	var st struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	if err := json.Unmarshal(statusJSON, &st); err != nil {
		return "", errors.New("tailscale status not readable")
	}
	if st.BackendState != "Running" {
		return "", fmt.Errorf("Tailscale is not connected (%s)", orDash(st.BackendState))
	}
	host := strings.TrimSuffix(st.Self.DNSName, ".")
	if !strings.HasSuffix(host, ".ts.net") {
		return "", errors.New("this computer has no ts.net name (turn on MagicDNS and HTTPS certificates in the Tailscale admin console)")
	}
	return host, nil
}

// funnelTarget reads "tailscale serve status --json": where https://host/ goes ("" = nowhere)
// and whether that port is open to the internet (Funnel) rather than the tailnet only.
func funnelTarget(serveJSON []byte, host string) (proxy string, public bool) {
	var sc struct {
		Web map[string]struct {
			Handlers map[string]struct{ Proxy string }
		}
		AllowFunnel map[string]bool
	}
	if json.Unmarshal(serveJSON, &sc) != nil {
		return "", false
	}
	key := host + ":443"
	return sc.Web[key].Handlers["/"].Proxy, sc.AllowFunnel[key]
}

// tsRun runs the tailscale CLI; a variable for tests.
var tsRun = func(timeout time.Duration, exe string, args ...string) (string, error) {
	return runQuiet(timeout, exe, args...)
}

// funnelEnsure makes sure https://<host>/ is funnelled to the link port and returns that base
// URL. A funnel on port 443 that already goes somewhere else is left alone (error), never replaced.
func funnelEnsure(exe string, port int) (string, error) {
	out, err := tsRun(10*time.Second, exe, "status", "--json", "--peers=false")
	if err != nil && strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("tailscale status: %v", err)
	}
	host, err := funnelHost([]byte(out))
	if err != nil {
		return "", err
	}
	want := fmt.Sprintf("http://127.0.0.1:%d", port)
	check := func() (string, bool) {
		s, _ := tsRun(10*time.Second, exe, "serve", "status", "--json")
		return funnelTarget([]byte(s), host)
	}
	proxy, public := check()
	if proxy == want && public {
		return "https://" + host, nil
	}
	if proxy != "" && proxy != want {
		return "", fmt.Errorf("https://%s already goes to %s; not replacing it", host, proxy)
	}
	// --bg: tailscaled keeps it (also after a reboot). When Funnel is not allowed for this
	// tailnet the command waits for the user to enable it, hence the timeout.
	set, err := tsRun(25*time.Second, exe, "funnel", "--bg", "--https=443", want)
	if proxy, public = check(); proxy == want && public {
		return "https://" + host, nil
	}
	msg := firstTextLine(set)
	if msg == "" && err != nil {
		msg = err.Error()
	}
	return "", fmt.Errorf("could not turn on Tailscale Funnel (it must be allowed in the tailnet's policy, with HTTPS certificates on): %s", msg)
}

// funnelOff removes our funnel, and only ours: nothing happens when https://<host>/ goes elsewhere.
func funnelOff(exe string, port int) {
	out, _ := tsRun(10*time.Second, exe, "status", "--json", "--peers=false")
	host, err := funnelHost([]byte(out))
	if err != nil {
		return
	}
	s, _ := tsRun(10*time.Second, exe, "serve", "status", "--json")
	if proxy, _ := funnelTarget([]byte(s), host); proxy != fmt.Sprintf("http://127.0.0.1:%d", port) {
		return
	}
	tsRun(15*time.Second, exe, "funnel", "--https=443", "off")
	serveLog("funnel: turned off")
}

func firstTextLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// funnelLoop is the supervisor's loop in funnel mode: set the funnel, publish the address, then
// look again every minute (tailscale signed out, funnel removed by hand...). When it ends because
// funnel mode was switched off the funnel is removed; when serve itself stops it stays, so the
// address answers again the moment serve is back.
func (t *tunnelSupervisor) funnelLoop(ctx context.Context, exe string, c linkConfig) {
	ready := waitLinkReady
	if t.ready != nil {
		ready = t.ready
	}
	ensure, off, still := t.funnel, t.funnelStop, t.funnelStill
	if ensure == nil {
		ensure = funnelEnsure
	}
	if off == nil {
		off = funnelOff
	}
	if still == nil {
		still = func() bool { return funnelMode(loadLinkCfg()) }
	}
	base := t.backoff
	if base == 0 {
		base = 2 * time.Second
	}
	t.saveState(tunnelState{State: "waiting", Error: fmt.Sprintf("waiting for port %d to listen", c.Port)})
	if !ready(ctx, c.Port) {
		return
	}
	url, lastErr, wait := "", "", base
	for ctx.Err() == nil {
		u, err := ensure(exe, c.Port)
		switch {
		case err != nil:
			if err.Error() != lastErr {
				lastErr = err.Error()
				serveLog("funnel: %s", lastErr)
			}
			t.saveState(tunnelState{State: "failed", Error: lastErr})
			if url != "" && t.onURL != nil {
				t.onURL("")
			}
			url = ""
			wait = min(wait*2, 2*time.Minute)
		case u != url:
			url, lastErr = u, ""
			t.saveState(tunnelState{State: "up", URL: u})
			if t.onURL != nil {
				t.onURL(u)
			}
			wait = 30 * base
		default:
			wait = 30 * base
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	if !still() {
		off(exe, c.Port)
	}
}

// tunnelUseFunnel switches link.json to the funnel and waits for serve to bring it up.
// It returns "" when the permanent address is up, else what is wrong.
func tunnelUseFunnel() string {
	if tailscaleExe() == "" {
		return "Tailscale is not installed; the permanent address uses Tailscale Funnel (https://tailscale.com/download)."
	}
	lc := loadLinkCfg()
	if !funnelMode(lc) {
		lc.Tunnel = "funnel"
		if err := writeJSONAtomic(linkCfgPath(), lc); err != nil {
			return "could not save link.json: " + err.Error()
		}
	}
	if !phoneHealth(fmt.Sprintf("http://127.0.0.1:%d", lc.Port), time.Second) {
		return "Permanent address turned on, but tokensaver serve is not running (tokensaver install-link installs it as a boot-time service)."
	}
	var st tunnelState
	for i := 0; i < 60; i++ {
		st = loadTunnelState()
		if strings.HasSuffix(st.URL, ".ts.net") && st.State == "up" {
			return ""
		}
		if st.State == "failed" && i > 12 { // serve re-reads link.json every 10 s
			break
		}
		time.Sleep(time.Second)
	}
	return "The permanent address is not up yet: " + orDash(st.Error)
}
