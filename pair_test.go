package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	tDesktopIP = "100.109.168.97"
	tLaptopIP  = "100.109.184.29"
	tTVIP      = "100.70.1.2"
	tOwner     = 42
	tTailnet   = "tail90c428.ts.net"
)

// fakeTailnet makes this machine the laptop and answers whois for a few devices.
func fakeTailnet(t *testing.T) *int {
	t.Helper()
	appHome = t.TempDir()
	mcpFails = &failLimiter{fails: map[string][]time.Time{}}
	writeJSONAtomic(linkCfgPath(), linkConfig{Token: "laptop-secret-token", Port: 8766})
	nodes := map[string]tsNode{
		tDesktopIP:   {Name: "ultimateviruscollection", Tailnet: tTailnet, UserID: tOwner, Login: "owner@example.com", OS: "windows", Online: true, IPs: []string{tDesktopIP}},
		tLaptopIP:    {Name: "omegajin-g1619-04", Tailnet: tTailnet, UserID: tOwner, Login: "owner@example.com", OS: "linux", Online: true, IPs: []string{tLaptopIP}},
		tTVIP:        {Name: "afonsinho-tv", Tailnet: tTailnet, UserID: tOwner, Login: "owner@example.com", OS: "android", Online: true, IPs: []string{tTVIP}},
		"100.80.0.1": {Name: "iris-s24-ultra", Tailnet: tTailnet, UserID: 7, Login: "someone@example.com", OS: "android", Online: true},
		"100.80.0.2": {Name: "ultimateviruscollection", Tailnet: "other.ts.net", UserID: 99, Login: "sharer@example.com", Shared: true},
	}
	calls := 0
	oldW, oldS, oldL := tsWhoisFn, tsSelfFn, localTSIPsFn
	tsWhoisFn = func(ip string) (tsNode, error) {
		calls++
		if n, ok := nodes[ip]; ok {
			return n, nil
		}
		return tsNode{}, errNoDevice
	}
	tsSelfFn = func() (tsSelfInfo, error) {
		return tsSelfInfo{Self: nodes[tLaptopIP], Peers: []tsNode{nodes[tDesktopIP], nodes[tTVIP]}}, nil
	}
	localTSIPsFn = func() []string { return []string{tLaptopIP} }
	t.Cleanup(func() { tsWhoisFn, tsSelfFn, localTSIPsFn = oldW, oldS, oldL })
	return &calls
}

var errNoDevice = &net.AddrError{Err: "no such device"}

func pairReq(remote, local string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/pair", nil)
	r.RemoteAddr = net.JoinHostPort(remote, "41000")
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(local), Port: 8766}))
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	linkHandler(loadLinkCfg()).ServeHTTP(w, r)
	return w
}

func TestPairAllowedDeviceGetsToken(t *testing.T) {
	fakeTailnet(t)
	w := pairReq(tDesktopIP, tLaptopIP, nil)
	if w.Code != 200 {
		t.Fatalf("allowed device: %d %s", w.Code, w.Body)
	}
	var rep pairReply
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil || rep.Token != "laptop-secret-token" || rep.Device != "omegajin-g1619-04" || rep.Port != 8766 {
		t.Fatalf("reply: %+v %v", rep, err)
	}
}

func TestPairRefusals(t *testing.T) {
	calls := fakeTailnet(t)
	cases := []struct {
		name, remote, local string
		hdr                 map[string]string
		whois               bool
	}{
		{"unlisted device on the tailnet", tTVIP, tLaptopIP, nil, true},
		{"allowlisted name, other user", "100.80.0.1", tLaptopIP, nil, true},
		{"shared-in device with an allowlisted name", "100.80.0.2", tLaptopIP, nil, true},
		{"unknown tailnet address", "100.99.9.9", tLaptopIP, nil, true},
		{"loopback (cloudflared, tailscale serve)", "127.0.0.1", "127.0.0.1", nil, false},
		{"LAN", "192.168.1.20", "192.168.1.10", nil, false},
		{"tailnet source but arrived on the LAN address", tDesktopIP, "192.168.1.10", nil, false},
		{"forwarded by a proxy", tDesktopIP, tLaptopIP, map[string]string{"X-Forwarded-For": "1.2.3.4"}, false},
		{"tailscale serve identity header", tDesktopIP, tLaptopIP, map[string]string{"Tailscale-User-Login": "owner@example.com"}, false},
	}
	for _, c := range cases {
		before := *calls
		w := pairReq(c.remote, c.local, c.hdr)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: want 403, got %d", c.name, w.Code)
		}
		if strings.Contains(w.Body.String(), "laptop-secret-token") {
			t.Errorf("%s: token leaked in the refusal", c.name)
		}
		if !c.whois && *calls != before {
			t.Errorf("%s: whois should not run for a connection that is not a direct tailnet one", c.name)
		}
	}
	r := httptest.NewRecorder()
	linkHandler(loadLinkCfg()).ServeHTTP(r, httptest.NewRequest("GET", "/pair", nil))
	if r.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /pair: %d", r.Code)
	}
}

func TestPairLogNeverHoldsToken(t *testing.T) {
	fakeTailnet(t)
	pairReq(tDesktopIP, tLaptopIP, nil)
	pairReq(tTVIP, tLaptopIP, nil)
	b, err := os.ReadFile(pairLogPath())
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	if !strings.Contains(log, "allowed ultimateviruscollection") || !strings.Contains(log, "refused "+tTVIP) || !strings.Contains(log, "allowlist") {
		t.Fatalf("pair log misses attempts:\n%s", log)
	}
	if strings.Contains(log, "laptop-secret-token") {
		t.Fatal("token in the pair log")
	}
}

func TestPairAllowlistFile(t *testing.T) {
	fakeTailnet(t)
	writeJSONAtomic(pairingCfgPath(), pairingConfig{Allow: []string{"Afonsinho-TV"}})
	if w := pairReq(tTVIP, tLaptopIP, nil); w.Code != 200 {
		t.Fatalf("device added in pairing.json: %d", w.Code)
	}
	if w := pairReq(tDesktopIP, tLaptopIP, nil); w.Code != 403 {
		t.Fatalf("device no longer in pairing.json: %d", w.Code)
	}
}

// TestPeerPairAndCall: the desktop pairs with the laptop over its /pair endpoint, stores the token
// (owner-only file), calls a tool there, and re-pairs by itself when the laptop's token changed.
func TestPeerPairAndCall(t *testing.T) {
	fakeTailnet(t)
	laptopView, _ := tsSelfFn()
	var onLaptop atomic.Bool // both sides share this process: the laptop's handler sees its own identity
	laptopSide := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		onLaptop.Store(true)
		defer onLaptop.Store(false)
		r.RemoteAddr = net.JoinHostPort(tDesktopIP, "41000") // what the laptop sees over WireGuard
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(tLaptopIP), Port: 8766}))
		linkHandler(loadLinkCfg()).ServeHTTP(w, r)
	})
	srv := httptest.NewServer(laptopSide)
	defer srv.Close()
	old := peerHTTP
	peerHTTP = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}}}
	defer func() { peerHTTP = old }()

	desktopView := tsSelfInfo{Self: tsNode{Name: "ultimateviruscollection", Tailnet: tTailnet, UserID: tOwner},
		Peers: []tsNode{{Name: "omegajin-g1619-04", Tailnet: tTailnet, UserID: tOwner, OS: "linux", Online: true, IPs: []string{tLaptopIP}}}}
	tsSelfFn = func() (tsSelfInfo, error) {
		if onLaptop.Load() {
			return laptopView, nil
		}
		return desktopView, nil
	}

	out, _, err := toolPeer(json.RawMessage(`{"action":"pair"}`))
	if err != nil || !strings.Contains(out, "omegajin-g1619-04: paired now") {
		t.Fatalf("pair all: %q %v", out, err)
	}
	if strings.Contains(out, "laptop-secret-token") {
		t.Fatal("token in tool output")
	}
	e := loadPeers()["omegajin-g1619-04"]
	if e.Token != "laptop-secret-token" || e.IP != tLaptopIP || e.Port != 8766 {
		t.Fatalf("stored peer: %+v", e)
	}
	if runtime.GOOS != "windows" {
		if fi, _ := os.Stat(peersPath()); fi.Mode().Perm() != 0o600 {
			t.Fatalf("peers.json mode %v", fi.Mode().Perm())
		}
	}
	st, _, _ := toolPeer(json.RawMessage(`{"action":"status"}`))
	if !strings.Contains(st, "omegajin-g1619-04") || !strings.Contains(st, "paired") || strings.Contains(st, "laptop-secret-token") {
		t.Fatalf("status: %s", st)
	}
	dir := t.TempDir()
	os.WriteFile(dir+"/hello.txt", []byte("hi"), 0o644)
	args, _ := json.Marshal(map[string]any{"action": "call", "peer": "omegajin-g1619-04", "tool": "scan", "args": map[string]any{"path": dir}})
	if out, _, err := toolPeer(args); err != nil || !strings.Contains(out, "1") {
		t.Fatalf("call: %q %v", out, err)
	}

	// the laptop's token changes: the next call re-pairs on its own
	writeJSONAtomic(linkCfgPath(), linkConfig{Token: "rotated-token", Port: 8766})
	if _, _, err := toolPeer(args); err != nil {
		t.Fatalf("call after rotation: %v", err)
	}
	if loadPeers()["omegajin-g1619-04"].Token != "rotated-token" {
		t.Fatal("not re-paired after the token changed")
	}
	if _, _, err := toolPeer(json.RawMessage(`{"action":"call","peer":"afonsinho-tv","tool":"scan"}`)); err == nil {
		t.Fatal("call to a device not on the tailnet list should fail")
	}
}

func TestParseTailscaleJSON(t *testing.T) {
	who, err := parseWhois([]byte(`{"Node":{"ID":1,"Name":"OmegaJin-G1619-04.tail90c428.ts.net.","User":42,"Addresses":["100.109.184.29/32","fd7a:115c:a1e0::1/128"],"Hostinfo":{"OS":"linux","Hostname":"spoofed"}},"UserProfile":{"ID":42,"LoginName":"owner@example.com"}}`))
	if err != nil || who.Name != "omegajin-g1619-04" || who.Tailnet != tTailnet || who.UserID != 42 || who.Shared || who.IPs[0] != tLaptopIP {
		t.Fatalf("whois: %+v %v", who, err)
	}
	shared, _ := parseWhois([]byte(`{"Node":{"Name":"x.other.ts.net.","User":42,"Sharer":9},"UserProfile":{"ID":42,"LoginName":"a"}}`))
	if !shared.Shared {
		t.Fatal("sharer not seen as shared")
	}
	if _, err := parseWhois([]byte(`no match`)); err == nil {
		t.Fatal("garbage whois accepted")
	}
	st, err := parseStatus([]byte(`{"BackendState":"Running","Self":{"DNSName":"ultimateviruscollection.tail90c428.ts.net.","OS":"windows","UserID":42,"TailscaleIPs":["100.109.168.97"]},"Peer":{"k1":{"DNSName":"omegajin-g1619-04.tail90c428.ts.net.","OS":"linux","UserID":42,"TailscaleIPs":["100.109.184.29"],"Online":true},"k2":{"DNSName":"note9.tail90c428.ts.net.","OS":"android","UserID":42,"Online":true}},"User":{"42":{"ID":42,"LoginName":"owner@example.com"}}}`))
	if err != nil || st.Self.Name != "ultimateviruscollection" || st.Self.Login != "owner@example.com" || len(st.Peers) != 2 {
		t.Fatalf("status: %+v %v", st, err)
	}
	if tg := pairTargets(st); len(tg) != 1 || tg[0].Name != "omegajin-g1619-04" {
		t.Fatalf("pair targets: %+v", tg)
	}
	if _, err := parseStatus([]byte(`{"BackendState":"Stopped","Self":{"DNSName":"a.b.","UserID":1}}`)); err == nil {
		t.Fatal("stopped tailscale accepted")
	}
}
