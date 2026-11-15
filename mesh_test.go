package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeMesh: this node is the laptop with a stubbed self entry and no real network.
func fakeMesh(t *testing.T) {
	t.Helper()
	fakeTailnet(t)
	oldSelf, oldProbe, oldCall, oldMagic, oldSleep, oldFwd := meshSelfFn, meshProbeFn, meshCallFn, sendMagicFn, meshSleep, adbForwardFn
	meshSelfFn = func() (string, peerEntry, bool) {
		return "omegajin-g1619-04", peerEntry{IP: tLaptopIP, Port: 8766, Token: "laptop-secret-token", Kind: "computer", Updated: time.Unix(1000, 0)}, false
	}
	meshSleep = func(time.Duration) {}
	adbForwardFn = func(string, int) (string, func(), error) { return "", nil, errors.New("adb not connected") }
	t.Cleanup(func() {
		meshSelfFn, meshProbeFn, meshCallFn, sendMagicFn, meshSleep, adbForwardFn = oldSelf, oldProbe, oldCall, oldMagic, oldSleep, oldFwd
	})
}

func TestMergePeers(t *testing.T) {
	old := time.Unix(100, 0)
	m := map[string]peerEntry{
		"tablet":       {IP: "100.76.11.44", Token: "t1", Kind: "android", LAN: "192.168.1.50", Port: 8765, Updated: old},
		"mcam-beelink": {IP: "100.104.221.41", Token: "m1", Port: 8766, Paired: time.Unix(500, 0)},
	}
	in := map[string]peerEntry{
		"Tablet":            {IP: "100.76.11.44", Token: "t2", Updated: time.Unix(200, 0)}, // newer: token and IP change, LAN kept
		"mcam-beelink":      {Token: "stale", Updated: time.Unix(300, 0)},                  // older than the local pairing
		"omegajin-g1619-04": {Token: "self", Updated: time.Unix(900, 0)},                   // this node
		"tv":                {IP: "100.70.1.2", Kind: "android", Token: "tv1"},             // new
		"empty":             {IP: "100.70.1.9"},                                            // no token
	}
	ch := mergePeers(m, in, "OmegaJin-G1619-04")
	if strings.Join(ch, ",") != "tablet,tv" {
		t.Fatalf("changed: %v", ch)
	}
	if e := m["tablet"]; e.Token != "t2" || e.LAN != "192.168.1.50" || e.Kind != "android" || e.Port != 8765 {
		t.Fatalf("tablet: %+v", e)
	}
	if m["mcam-beelink"].Token != "m1" {
		t.Fatal("an older announcement replaced a newer pairing")
	}
	if _, ok := m["omegajin-g1619-04"]; ok {
		t.Fatal("own entry stored")
	}
	if m["tv"].Port != androidPort {
		t.Fatalf("android default port: %d", m["tv"].Port)
	}
	if _, ok := m["empty"]; ok {
		t.Fatal("entry without token stored")
	}
	if ch := mergePeers(m, in, "omegajin-g1619-04"); len(ch) != 0 {
		t.Fatalf("second merge changed %v", ch)
	}
}

func TestMeshSyncEndpoint(t *testing.T) {
	fakeMesh(t)
	savePeer("mcam-beelink", peerEntry{IP: "100.104.221.41", Port: 8766, Token: "michie-token", Paired: time.Now()})
	h := linkHandler(loadLinkCfg())
	body, _ := json.Marshal(meshSync{From: "Tablet", Self: peerEntry{IP: "100.76.11.44", Token: "tablet-token", Kind: "android", Updated: time.Now()},
		Peers: map[string]peerEntry{"iris-s24-ultra": {IP: "100.84.75.31", Token: "phone-token", Kind: "android", Updated: time.Now()}}})

	bad := httptest.NewRequest("POST", "/peers/sync", bytes.NewReader(body))
	bad.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, bad)
	if w.Code != 401 || strings.Contains(w.Body.String(), "michie-token") {
		t.Fatalf("bad token: %d %s", w.Code, w.Body.String())
	}
	if _, ok := loadPeers()["tablet"]; ok {
		t.Fatal("merged without a valid token")
	}

	r := httptest.NewRequest("POST", "/peers/sync", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer laptop-secret-token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("sync: %d %s", w.Code, w.Body.String())
	}
	var rep meshSync
	json.Unmarshal(w.Body.Bytes(), &rep)
	if rep.From != "omegajin-g1619-04" || rep.Self.Token != "laptop-secret-token" || rep.Peers["mcam-beelink"].Token != "michie-token" || rep.You != "tablet" {
		t.Fatalf("reply: %+v", rep)
	}
	p := loadPeers()
	if p["tablet"].Token != "tablet-token" || p["iris-s24-ultra"].Token != "phone-token" {
		t.Fatalf("not merged: %+v", p)
	}
	b, _ := os.ReadFile(pairLogPath())
	for _, tok := range []string{"tablet-token", "phone-token", "michie-token", "laptop-secret-token"} {
		if strings.Contains(string(b), tok) {
			t.Fatal("token in pair.log")
		}
	}
}

func TestMeshRelayOrder(t *testing.T) {
	fakeMesh(t)
	m := map[string]peerEntry{
		"ultimateviruscollection": {Token: "a"}, "mcam-beelink": {Token: "b"}, "tablet": {Token: "c", Kind: "android"},
		"tv": {Token: "d", Kind: "android"}, "target": {Token: "e"}, "notoken": {},
	}
	got := strings.Join(meshRelays("target", "omegajin-g1619-04", m), ",")
	if got != "ultimateviruscollection,tablet,tv,mcam-beelink" {
		t.Fatalf("relay order: %s", got)
	}
}

// The desktop is asleep and unreachable directly: the call goes through the tablet's peer tool,
// one hop, never further.
func TestMeshCallViaRelay(t *testing.T) {
	fakeMesh(t)
	savePeer("ultimateviruscollection", peerEntry{IP: tDesktopIP, LAN: "192.168.1.10", Port: 8766, Token: "desk-token", Paired: time.Now()})
	savePeer("tablet", peerEntry{IP: "100.76.11.44", Port: 8765, Token: "tablet-token", Kind: "android", Paired: time.Now()})
	meshProbeFn = func(base string, _ time.Duration) string {
		if strings.Contains(base, "100.76.11.44") {
			return ""
		}
		return "timeout"
	}
	var calls []string
	meshCallFn = func(_ *http.Client, base, token, tool string, args json.RawMessage) (string, []toolImage, int, error) {
		calls = append(calls, base+" "+tool+" "+string(args))
		if token != "tablet-token" {
			t.Fatalf("wrong token for %s", base)
		}
		var a map[string]any
		json.Unmarshal(args, &a)
		if tool != "peer" || a["peer"] != "ultimateviruscollection" || a["hops"] != float64(1) || a["tool"] != "system" {
			t.Fatalf("relay args: %s %s", tool, args)
		}
		return "desktop info", nil, 200, nil
	}
	out, _, err := toolPeer(json.RawMessage(`{"action":"call","peer":"ultimateviruscollection","tool":"system","args":{"action":"info"}}`))
	if err != nil || !strings.Contains(out, "desktop info") || !strings.Contains(out, "via relay tablet") {
		t.Fatalf("call: %q %v", out, err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls: %v", calls)
	}
}

// Nothing answers, no relay can help: the computer is woken and the call retried after it comes up.
func TestMeshCallWakesAndRetries(t *testing.T) {
	fakeMesh(t)
	savePeer("ultimateviruscollection", peerEntry{IP: tDesktopIP, LAN: "192.168.1.10", MAC: "aa:bb:cc:dd:ee:ff", Port: 8766, Token: "desk-token", Paired: time.Now()})
	woken := false
	sendMagicFn = func(mac, lan string) error {
		if mac != "aa:bb:cc:dd:ee:ff" || lan != "192.168.1.10" {
			t.Fatalf("magic packet %s %s", mac, lan)
		}
		woken = true
		return nil
	}
	probes := 0
	meshProbeFn = func(base string, _ time.Duration) string {
		if woken {
			probes++
			if probes > 2 {
				return ""
			}
		}
		return "timeout"
	}
	meshCallFn = func(_ *http.Client, base, token, tool string, _ json.RawMessage) (string, []toolImage, int, error) {
		return "ok from " + base, nil, 200, nil
	}
	out, _, err := toolPeer(json.RawMessage(`{"action":"call","peer":"ultimateviruscollection","tool":"scan"}`))
	if err != nil || !woken || !strings.Contains(out, "after wake") {
		t.Fatalf("call: %q %v woken=%v", out, err, woken)
	}
}

// A relayed call (hops 1) never relays again and never wakes.
func TestMeshRelayedCallStaysOneHop(t *testing.T) {
	fakeMesh(t)
	savePeer("ultimateviruscollection", peerEntry{IP: tDesktopIP, MAC: "aa:bb:cc:dd:ee:ff", Port: 8766, Token: "desk-token", Paired: time.Now()})
	savePeer("tablet", peerEntry{IP: "100.76.11.44", Port: 8765, Token: "tablet-token", Kind: "android", Paired: time.Now()})
	meshProbeFn = func(string, time.Duration) string { return "timeout" }
	meshCallFn = func(*http.Client, string, string, string, json.RawMessage) (string, []toolImage, int, error) {
		t.Fatal("no call expected")
		return "", nil, 0, nil
	}
	sendMagicFn = func(string, string) error { t.Fatal("no wake on a relayed call"); return nil }
	_, _, err := toolPeer(json.RawMessage(`{"action":"call","peer":"ultimateviruscollection","tool":"scan","hops":1}`))
	if err == nil || !strings.Contains(err.Error(), errUnreachablePrefix) {
		t.Fatalf("want unreachable, got %v", err)
	}
	if _, _, err := toolPeer(json.RawMessage(`{"action":"call","peer":"tablet","tool":"peer"}`)); err == nil {
		t.Fatal("peer tool called directly")
	}
}

func TestMeshStatusHidesTokens(t *testing.T) {
	fakeMesh(t)
	savePeer("tablet", peerEntry{IP: "100.76.11.44", LAN: "192.168.1.50", Port: 8765, Token: "tablet-token", Kind: "android", Paired: time.Now()})
	meshProbeFn = func(base string, _ time.Duration) string {
		if strings.Contains(base, "192.168.1.50") {
			return ""
		}
		return "timeout"
	}
	out := meshStatus()
	if !strings.Contains(out, "tablet (android): lan up, tailscale timeout") || strings.Contains(out, "tablet-token") {
		t.Fatalf("status: %s", out)
	}
}

func TestPairAllowAdd(t *testing.T) {
	fakeMesh(t)
	if _, _, err := toolPeer(json.RawMessage(`{"action":"allow","peer":"Afonsinho-TV"}`)); err != nil {
		t.Fatal(err)
	}
	if !pairAllowed("afonsinho-tv") || !pairAllowed("ultimateviruscollection") {
		t.Fatalf("allowlist: %v", pairAllowlist())
	}
}

func TestCleanCLIXML(t *testing.T) {
	in := "before\r\n#< CLIXML\r\n<Objs Version=\"1.1.0.1\" xmlns=\"http://schemas.microsoft.com/powershell/2004/04\"><Obj S=\"progress\" RefId=\"0\"><TN RefId=\"0\"><T>System.Management.Automation.PSCustomObject</T></TN><MS><I64 N=\"SourceId\">1</I64><PR N=\"Record\"><AV>Preparing modules for first use.</AV></PR></MS></Obj><S S=\"Error\">Get-Item : Cannot find path &apos;C:\\x&apos;_x000D__x000A_</S></Objs>\r\nafter"
	out := cleanCLIXML(in)
	if strings.Contains(out, "CLIXML") || strings.Contains(out, "Preparing modules") || strings.Contains(out, "<Objs") {
		t.Fatalf("noise left: %q", out)
	}
	if !strings.Contains(out, "Get-Item : Cannot find path 'C:\\x'") || !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Fatalf("lost text: %q", out)
	}
	if cleanCLIXML("plain") != "plain" {
		t.Fatal("plain text changed")
	}
}
