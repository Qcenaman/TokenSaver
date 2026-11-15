package main

// mesh.go: one shared peer registry for every TokenSaver and MobileTokenSaver node (desktop,
// laptop, Michie Pc, phone, tablet, TV), so no token is ever copied by hand.
//
//   - Every node keeps the registry in peers.json (owner-only): name → Tailscale IP, LAN IP, MAC,
//     port, token, kind, tunnel, adb serial, and when the device itself last changed its link.
//   - POST /peers/sync (Bearer = this node's own token) swaps registries: whoever already holds a
//     node's token may hand it every link it knows and gets that node's list back. The newest
//     announcement of a device wins, so a changed token or IP spreads by itself (self-heal).
//   - Computers still pair over Tailscale (/pair, pair.go); Android devices are adopted over adb
//     (their app hands its token to adb, as phone pair does) and then sync like everyone else.
//   - The peer tool reaches a node over LAN → Tailscale → tunnel → relay through another node →
//     adb (Android) → Wake-on-LAN (here and from the relays on the home network), then retries.
//
// Tokens never go to logs or tool output.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const androidPort = 8765

// meshSync is the body of POST /peers/sync, both ways.
type meshSync struct {
	From    string               `json:"from"`
	Self    peerEntry            `json:"self"`
	Peers   map[string]peerEntry `json:"peers"`
	You     string               `json:"you,omitempty"`     // the name the sender knows the receiver by
	Primary string               `json:"primary,omitempty"` // the computer Android devices use as "the PC"
	PC      string               `json:"pc,omitempty"`      // Android: the name of the PC its pc tool uses ("" = none yet)
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func effTime(e peerEntry) time.Time {
	if e.Updated.After(e.Paired) {
		return e.Updated
	}
	return e.Paired
}

func normName(n string) string { return strings.ToLower(strings.TrimSpace(n)) }

// mergePeers folds incoming entries into m: an entry is taken when m has none for that name or
// the incoming one is newer; fields the newer one leaves empty keep their old value. self is
// never stored. Returns the names that changed.
func mergePeers(m map[string]peerEntry, in map[string]peerEntry, self string) []string {
	var changed []string
	self = normName(self)
	for name, e := range in {
		name = normName(name)
		if name == "" || name == self || e.Token == "" {
			continue
		}
		cur, ok := m[name]
		if ok && !effTime(e).After(effTime(cur)) {
			continue
		}
		if ok {
			if e.IP == "" {
				e.IP = cur.IP
			}
			if e.LAN == "" {
				e.LAN = cur.LAN
			}
			if e.MAC == "" {
				e.MAC = cur.MAC
			}
			if e.Kind == "" {
				e.Kind = cur.Kind
			}
			if e.Tunnel == "" {
				e.Tunnel = cur.Tunnel
			}
			if e.Adb == "" {
				e.Adb = cur.Adb
			}
			if e.Port == 0 {
				e.Port = cur.Port
			}
		}
		if e.Port == 0 {
			e.Port = 8766
			if e.Kind == "android" {
				e.Port = androidPort
			}
		}
		if ok && e == cur {
			continue
		}
		m[name] = e
		changed = append(changed, name)
	}
	sort.Strings(changed)
	return changed
}

// updatePeers changes peers.json under its lock and writes it back when fn reports a change.
func updatePeers(fn func(m map[string]peerEntry) bool) error {
	peersMu.Lock()
	defer peersMu.Unlock()
	m := loadPeers()
	if !fn(m) {
		return nil
	}
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return err
	}
	p := peersPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func mergeIntoPeers(in map[string]peerEntry, self string) []string {
	var ch []string
	updatePeers(func(m map[string]peerEntry) bool {
		ch = mergePeers(m, in, self)
		return len(ch) > 0
	})
	return ch
}

// --- this device ------------------------------------------------------------------------------

type meshSelfState struct {
	FP      string    `json:"fp"`
	Updated time.Time `json:"updated"`
}

func meshSelfPath() string { return filepath.Join(appHome, "mesh-self.json") }

var meshSelfMu sync.Mutex

// meshSelfName: this device's Tailscale machine name, else its host name.
func meshSelfName() string {
	if s, err := tsSelfFn(); err == nil && s.Self.Name != "" {
		return s.Self.Name
	}
	h, _ := os.Hostname()
	return normName(h)
}

// meshSelfFn is this device's own registry entry; changed is true when its link differs from
// the last look (the time it changed is kept in mesh-self.json). A variable for tests.
var meshSelfFn = func() (string, peerEntry, bool) {
	name := meshSelfName()
	lc := loadLinkCfg()
	e := peerEntry{Port: lc.Port, Token: lc.Token, Kind: "computer", Tunnel: currentTunnelURL()}
	if s, err := tsSelfFn(); err == nil {
		e.IP = peerIP(s.Self)
	}
	if e.IP == "" {
		if ips := tailscaleIPs(); len(ips) > 0 {
			e.IP = ips[0]
		}
	}
	e.LAN, e.MAC = localRouteTo("")
	h := sha256.Sum256([]byte(strings.Join([]string{name, e.IP, e.LAN, e.MAC, fmt.Sprint(e.Port), e.Token, e.Tunnel}, "|")))
	fp := hex.EncodeToString(h[:8])
	meshSelfMu.Lock()
	defer meshSelfMu.Unlock()
	var st meshSelfState
	if b, err := os.ReadFile(meshSelfPath()); err == nil {
		json.Unmarshal(b, &st)
	}
	changed := false
	if st.FP != fp {
		changed = st.FP != ""
		st = meshSelfState{FP: fp, Updated: time.Now()}
		writeJSONAtomic(meshSelfPath(), st)
	}
	e.Updated, e.Paired = st.Updated, st.Updated
	return name, e, changed
}

func meshSelf() (string, peerEntry, bool) { return meshSelfFn() }

// meshPrimary: the computer Android devices treat as "the PC" (pairing.json "primary", else the
// first name on the allowlist: the desktop).
func meshPrimary() string {
	var c struct {
		Primary string `json:"primary"`
	}
	if b, err := os.ReadFile(pairingCfgPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if p := normName(c.Primary); p != "" {
		return p
	}
	if l := pairAllowlist(); len(l) > 0 {
		return l[0]
	}
	return ""
}

// --- the /peers/sync endpoint -----------------------------------------------------------------

func handleMeshSync(w http.ResponseWriter, r *http.Request) {
	src := clientIP(r)
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if mcpFails.blocked(src) {
		http.Error(w, "too many failures", http.StatusTooManyRequests)
		return
	}
	if tok := loadLinkCfg().Token; tok == "" || bearer(r) != tok {
		mcpFails.fail(src)
		pairLog("mesh sync refused %s: bad token", src)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var in meshSync
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	name, self, _ := meshSelf()
	ch := mergeIntoPeers(meshIncoming(in), name)
	pairLog("mesh sync with %s (%s): %d updated %s", orDash(in.From), src, len(ch), strings.Join(ch, " "))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(meshSync{From: name, Self: self, Peers: loadPeers(), You: normName(in.From), Primary: meshPrimary()})
}

// meshIncoming: the sender's peers plus its own entry under its name.
func meshIncoming(in meshSync) map[string]peerEntry {
	m := map[string]peerEntry{}
	for k, v := range in.Peers {
		m[k] = v
	}
	if f := normName(in.From); f != "" && in.Self.Token != "" {
		m[f] = in.Self
	}
	return m
}

// --- routes -----------------------------------------------------------------------------------

type meshRoute struct {
	Via     string
	Base    string
	Timeout time.Duration
}

func meshRoutes(e peerEntry) []meshRoute {
	var out []meshRoute
	port := e.Port
	if port == 0 {
		port = 8766
	}
	if e.LAN != "" {
		out = append(out, meshRoute{"lan", fmt.Sprintf("http://%s:%d", e.LAN, port), 1500 * time.Millisecond})
	}
	if e.IP != "" {
		out = append(out, meshRoute{"tailscale", fmt.Sprintf("http://%s:%d", e.IP, port), 6 * time.Second})
	}
	if t := strings.TrimRight(strings.TrimSpace(e.Tunnel), "/"); t != "" {
		if !strings.HasPrefix(t, "http://") && !strings.HasPrefix(t, "https://") {
			t = "https://" + t
		}
		out = append(out, meshRoute{"tunnel", t, 8 * time.Second})
	}
	return out
}

// meshProbeFn: "" when base/health answers 200, else a few words why not. A variable for tests.
var meshProbeFn = func(base string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/health", nil)
	resp, err := peerHTTP.Do(req)
	if err != nil {
		m := strings.ToLower(err.Error())
		switch {
		case strings.Contains(m, "deadline") || strings.Contains(m, "timeout"):
			return "timeout"
		case strings.Contains(m, "refused"):
			return "refused"
		case strings.Contains(m, "unreachable") || strings.Contains(m, "no route"):
			return "no route"
		}
		return "error"
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return ""
}

// meshReachDirect: the first route that answers, and what each route said.
func meshReachDirect(e peerEntry) (base, via string, tried []string) {
	for _, r := range meshRoutes(e) {
		why := meshProbeFn(r.Base, r.Timeout)
		if why == "timeout" && r.Via == "tailscale" { // an idle Tailscale path needs a moment to wake
			why = meshProbeFn(r.Base, 2*r.Timeout)
		}
		if why == "" {
			return r.Base, r.Via, tried
		}
		tried = append(tried, r.Via+" "+why)
	}
	if len(tried) == 0 {
		tried = []string{"no address known"}
	}
	return "", "", tried
}

// --- calling ----------------------------------------------------------------------------------

// errUnreachablePrefix starts every "could not reach it" error, so a relay's answer tells
// "the relay can't reach it either" apart from "the tool on it failed".
const errUnreachablePrefix = "peer-unreachable: "

var meshCallFn = mcpHTTPCall // a variable for tests

// meshRelays: other registry nodes to relay through — the primary computer first (strongest),
// then the Android devices (the tablet and TV are always on), then the other computers.
func meshRelays(target, self string, m map[string]peerEntry) []string {
	var out []string
	for n, e := range m {
		if n != target && n != self && e.Token != "" {
			out = append(out, n)
		}
	}
	prim := meshPrimary()
	rank := func(n string) int {
		switch {
		case n == prim:
			return 0
		case m[n].Kind == "android":
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		return out[i] < out[j]
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

func meshLookup(name string) (string, peerEntry, bool) {
	name = normName(name)
	m := loadPeers()
	if e, ok := m[name]; ok {
		return name, e, true
	}
	for n, e := range m { // an IP works as a name too
		if e.IP == name || e.LAN == name {
			return n, e, true
		}
	}
	return name, peerEntry{}, false
}

// meshRefresh tries to get a working token for name after a 401: pair again (computers), take
// phone.json's token (the paired phone), or ask the other nodes for their newer list.
func meshRefresh(name string, e peerEntry, askOthers bool) (peerEntry, bool) {
	if e.Kind != "android" {
		if s, err := tsSelfFn(); err == nil {
			if p, err := findPeer(s, name); err == nil && pairWith(p) == nil {
				_, ne, _ := meshLookup(name)
				return ne, true
			}
		}
	}
	if pc := loadPhoneCfg(); pc.Token != "" && pc.Token != e.Token && (pc.TSIP == e.IP || pc.LanIP == e.LAN) {
		e.Token, e.Paired = pc.Token, time.Now()
		savePeer(name, e)
		return e, true
	}
	if !askOthers {
		return e, false
	}
	meshSyncAll()
	if ne, ok := loadPeers()[name]; ok && ne.Token != e.Token {
		return ne, true
	}
	return e, false
}

// callBase runs tool at base with e's token, refreshing the token once on a 401.
func meshCallBase(name string, e peerEntry, base, tool string, args json.RawMessage) (string, []toolImage, error) {
	text, imgs, code, err := meshCallFn(peerHTTP, base, e.Token, tool, args)
	if code == 401 {
		ne, ok := meshRefresh(name, e, true)
		if !ok {
			return "", nil, fmt.Errorf("%s rejected the stored token and no newer one was found (pair it again: peer adopt for Android, peer pair for a computer)", name)
		}
		text, imgs, code, err = meshCallFn(peerHTTP, base, ne.Token, tool, args)
	}
	if err != nil && code == 0 {
		return "", nil, fmt.Errorf("%s%s: %v", errUnreachablePrefix, name, err)
	}
	return text, imgs, err
}

// adbForwardFn opens a local port forwarded over adb to the device's engine; close removes it.
var adbForwardFn = func(serial string, port int) (base string, closeFn func(), err error) {
	if serial == "" || !adbFound() || !adbEnsure(serial) {
		return "", nil, errors.New("adb not connected")
	}
	out, err := adbRun(10*time.Second, "-s", serial, "forward", "tcp:0", fmt.Sprintf("tcp:%d", port))
	lp := strings.TrimSpace(out)
	if err != nil || lp == "" {
		return "", nil, fmt.Errorf("adb forward failed: %v", err)
	}
	return "http://127.0.0.1:" + lp, func() { adbRun(5*time.Second, "-s", serial, "forward", "--remove", "tcp:"+lp) }, nil
}

func meshAdbSerial(e peerEntry) string {
	if e.Adb != "" {
		return e.Adb
	}
	if e.IP != "" {
		return net.JoinHostPort(e.IP, "5555")
	}
	return ""
}

var meshSleep = time.Sleep

// meshBackoff: the waits between probes after a wake (about 90 s in all).
var meshBackoff = []time.Duration{5 * time.Second, 10 * time.Second, 15 * time.Second, 20 * time.Second, 20 * time.Second, 20 * time.Second}

// meshCall runs tool on the node called name over the first way that works. hops > 0 means
// this call came from another node's relay: then no further relay and no waking.
func meshCall(name, tool string, args json.RawMessage, hops int) (string, []toolImage, string, error) {
	name, e, ok := meshLookup(name)
	if !ok {
		return "", nil, "", fmt.Errorf("%s%s is not in this node's registry (peer status lists who is; peer sync asks the others)", errUnreachablePrefix, name)
	}
	var notes []string
	try := func(base, via string) (string, []toolImage, string, error, bool) {
		text, imgs, err := meshCallBase(name, e, base, tool, args)
		if err != nil && strings.HasPrefix(err.Error(), errUnreachablePrefix) {
			notes = append(notes, via+" dropped")
			return "", nil, "", nil, false
		}
		return text, imgs, via, err, true
	}
	if base, via, tried := meshReachDirect(e); base != "" {
		if t, i, v, err, done := try(base, via); done {
			return t, i, v, err
		}
	} else {
		notes = append(notes, tried...)
	}
	self := meshSelfName()
	if hops == 0 {
		for _, r := range meshRelays(name, self, loadPeers()) {
			t, i, err := meshViaRelay(r, name, tool, args)
			if !relayUnable(err) {
				return t, i, "relay " + r, err
			}
			notes = append(notes, "relay "+r+" no")
		}
	}
	if e.Kind == "android" {
		if base, closeFn, err := adbForwardFn(meshAdbSerial(e), e.Port); err == nil {
			t, i, v, cerr, done := try(base, "adb")
			closeFn()
			if done {
				return t, i, v, cerr
			}
		} else {
			notes = append(notes, "adb "+err.Error())
		}
	}
	if hops == 0 && e.MAC != "" {
		notes = append(notes, meshWake(name, e, 0))
		for _, d := range meshBackoff {
			meshSleep(d)
			if base, via, _ := meshReachDirect(e); base != "" {
				if t, i, v, err, done := try(base, via+" after wake"); done {
					return t, i, v, err
				}
			}
		}
		notes = append(notes, "still down ~90 s after the wake")
	}
	return "", nil, "", fmt.Errorf("%s%s: %s", errUnreachablePrefix, name, strings.Join(notes, "; "))
}

// relayUnable: the relay could not reach the target, or runs a version that can't relay.
func relayUnable(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, errUnreachablePrefix) || strings.Contains(m, "not its peer tool") ||
		strings.Contains(m, "unknown tool") || strings.Contains(m, "not in this node's registry")
}

// meshViaRelay asks relay's peer tool to run tool on target (one hop, no further relays).
func meshViaRelay(relay, target, tool string, args json.RawMessage) (string, []toolImage, error) {
	_, r, ok := meshLookup(relay)
	if !ok {
		return "", nil, errors.New(errUnreachablePrefix + relay + " unknown")
	}
	base, _, _ := meshReachDirect(r)
	if base == "" {
		return "", nil, errors.New(errUnreachablePrefix + relay + " itself unreachable")
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	ra, _ := json.Marshal(map[string]any{"action": "call", "peer": target, "tool": tool, "args": args, "hops": 1})
	return meshCallBase(relay, r, base, "peer", ra)
}

// sendMagicFn sends a Wake-on-LAN packet for mac to the broadcast of lan's /24 and to
// 255.255.255.255 (UDP 9 and 7). A variable for tests.
var sendMagicFn = func(mac, lan string) error {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return fmt.Errorf("bad MAC %q", mac)
	}
	pkt := bytes.Repeat([]byte{0xff}, 6)
	for i := 0; i < 16; i++ {
		pkt = append(pkt, hw...)
	}
	targets := []string{"255.255.255.255"}
	if ip := ipv4(lan); ip != nil {
		targets = append([]string{fmt.Sprintf("%d.%d.%d.255", ip[0], ip[1], ip[2])}, targets...)
	}
	sent := 0
	for _, t := range targets {
		for _, port := range []string{"9", "7"} {
			c, err := net.Dial("udp", net.JoinHostPort(t, port))
			if err != nil {
				continue
			}
			if _, err := c.Write(pkt); err == nil {
				sent++
			}
			c.Close()
		}
	}
	if sent == 0 {
		return errors.New("no packet could be sent")
	}
	return nil
}

// meshWake sends the magic packet from here and (hops 0) from every relay that answers, since
// one of them is usually on the home network when this node isn't. Returns one line.
func meshWake(name string, e peerEntry, hops int) string {
	if e.MAC == "" {
		return name + ": MAC unknown (it announces it at its next sync)"
	}
	var parts []string
	if err := sendMagicFn(e.MAC, e.LAN); err != nil {
		parts = append(parts, "here: "+err.Error())
	} else {
		parts = append(parts, "here")
	}
	if hops == 0 {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, r := range meshRelays(name, meshSelfName(), loadPeers()) {
			wg.Add(1)
			go func(r string) {
				defer wg.Done()
				_, re, _ := meshLookup(r)
				base, _, _ := meshReachDirect(re)
				if base == "" {
					return
				}
				a, _ := json.Marshal(map[string]any{"action": "wake", "peer": name, "hops": 1})
				if _, _, err := meshCallBase(r, re, base, "peer", a); err == nil {
					mu.Lock()
					parts = append(parts, r)
					mu.Unlock()
				}
			}(r)
		}
		wg.Wait()
	}
	return "magic packet for " + name + " sent from " + strings.Join(parts, ", ")
}

// --- syncing ----------------------------------------------------------------------------------

// meshSyncWith swaps registries with one node over its direct routes.
func meshSyncWith(name string, e peerEntry) error {
	base, _, tried := meshReachDirect(e)
	if base == "" {
		return fmt.Errorf("unreachable (%s)", strings.Join(tried, ", "))
	}
	selfName, self, _ := meshSelf()
	body, _ := json.Marshal(meshSync{From: selfName, Self: self, Peers: loadPeers(), You: name, Primary: meshPrimary()})
	do := func(tok string) (*http.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "POST", base+"/peers/sync", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := peerHTTP.Do(req)
		if err != nil {
			cancel()
			return nil, err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		cancel()
		resp.Body = io.NopCloser(bytes.NewReader(b))
		return resp, nil
	}
	resp, err := do(e.Token)
	if err != nil {
		return err
	}
	if resp.StatusCode == 401 {
		ne, ok := meshRefresh(name, e, false)
		if !ok {
			return errors.New("token rejected")
		}
		if resp, err = do(ne.Token); err != nil {
			return err
		}
	}
	switch resp.StatusCode {
	case 200:
	case 404:
		return errors.New("its TokenSaver has no registry sync yet (update it)")
	default:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var rep meshSync
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return errors.New("bad sync reply")
	}
	if f := normName(rep.From); f != "" && f != name && rep.Self.Token != "" && e.Kind == "android" {
		// it calls itself something else (an Android device stored under a guessed name)
		updatePeers(func(m map[string]peerEntry) bool {
			delete(m, name)
			return true
		})
	}
	ch := mergeIntoPeers(meshIncoming(rep), selfName)
	if len(ch) > 0 {
		pairLog("mesh sync with %s: %d updated %s", name, len(ch), strings.Join(ch, " "))
	}
	// An Android device without a PC yet gets the primary computer's link for its pc tool.
	if e.Kind == "android" && rep.PC == "" && selfName == meshPrimary() {
		if err := meshPushLink(base, e.Token, e.LAN); err != nil {
			pairLog("mesh: %s could not store the PC link: %v", name, err)
		}
	}
	return nil
}

func meshPushLink(base, token, lan string) error {
	lc := loadLinkCfg()
	_, mac := localRouteTo(lan)
	host, _ := os.Hostname()
	body, _ := json.Marshal(buildLinkJSON(lc, currentRoutes(lc, lan), mac, host))
	req, _ := http.NewRequest("POST", base+"/link", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := phoneHTTP.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// meshSyncAll syncs with every registry node at once; one line per node.
func meshSyncAll() []string {
	m := loadPeers()
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]string, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			if err := meshSyncWith(n, m[n]); err != nil {
				out[i] = n + ": " + err.Error()
			} else {
				out[i] = n + ": synced"
			}
		}(i, n)
	}
	wg.Wait()
	return out
}

// meshTick: what serve does every 10 minutes (and at once when this device's link changed).
func meshTick() []string {
	var out []string
	out = append(out, meshImportPhone()...)
	out = append(out, meshAutoAdopt()...)
	return append(out, meshSyncAll()...)
}

// meshImportPhone puts the phone paired with phone pair into the registry once.
func meshImportPhone() []string {
	pc := loadPhoneCfg()
	if pc.Token == "" {
		return nil
	}
	for _, e := range loadPeers() {
		if e.Token == pc.Token || (pc.TSIP != "" && e.IP == pc.TSIP) {
			return nil
		}
	}
	name := ""
	if pc.TSIP != "" {
		if w, err := tsWhoisFn(pc.TSIP); err == nil {
			name = w.Name
		}
	}
	if name == "" {
		name = "phone"
	}
	savePeer(name, peerEntry{IP: pc.TSIP, LAN: pc.LanIP, Port: pc.Port, Token: pc.Token, Kind: "android", Adb: adbTCPTarget(pc), Paired: time.Now()})
	return []string{name + ": added from phone.json"}
}

// meshAutoAdopt adopts every online Android device of the tailnet's owner that is on the
// allowlist or already authorized for this computer's adb, and not yet in the registry.
func meshAutoAdopt() []string {
	if !adbFound() {
		return nil
	}
	s, err := tsSelfFn()
	if err != nil {
		return nil
	}
	m := loadPeers()
	devs, _ := adbRun(8*time.Second, "devices")
	authorized := adbStates(devs)
	var out []string
	for _, p := range s.Peers {
		if p.OS != "android" || !p.Online || !sameOwner(s.Self, p) {
			continue
		}
		if _, ok := m[p.Name]; ok {
			continue
		}
		ip := peerIP(p)
		serial := net.JoinHostPort(ip, "5555")
		if !pairAllowed(p.Name) && authorized[serial] != "device" {
			continue
		}
		msg, err := meshAdopt(serial, p.Name)
		if err != nil {
			pairLog("mesh adopt %s: %v", p.Name, err)
			continue
		}
		out = append(out, msg)
	}
	return out
}

var devNameRe = regexp.MustCompile(`[^a-z0-9-]+`)

// meshAdopt takes an Android device's MobileTokenSaver into the registry over adb and swaps
// registries with it. name may be empty (then Tailscale, else the device name, decides).
func meshAdopt(serial, name string) (string, error) {
	if !adbFound() {
		return "", errors.New("adb not found on this computer")
	}
	if !adbEnsure(serial) {
		return "", fmt.Errorf("adb can't connect to %s", serial)
	}
	pkg := loadPhoneCfg().Package
	tok := phoneAppToken(serial, pkg)
	if tok == "" {
		return "", errors.New("MobileTokenSaver gave no token over adb (" + phoneTokenHelp + ")")
	}
	ts := phoneTailscaleIP(serial)
	if name == "" && ts != "" {
		if w, err := tsWhoisFn(ts); err == nil {
			name = w.Name
		}
	}
	if name == "" {
		out, _ := adbShell(serial, 8*time.Second, "settings get global device_name")
		name = strings.Trim(devNameRe.ReplaceAllString(normName(out), "-"), "-")
	}
	if name == "" {
		return "", errors.New("could not tell the device's name")
	}
	e := peerEntry{IP: ts, Port: androidPort, Token: tok, Kind: "android", Adb: serial, Paired: time.Now()}
	if old, ok := loadPeers()[name]; ok {
		e.LAN, e.Updated = old.LAN, old.Updated
	}
	if err := savePeer(name, e); err != nil {
		return "", err
	}
	pairLog("mesh adopted %s over adb", name)
	msg := name + ": adopted over adb"
	if err := meshSyncWith(name, e); err != nil {
		msg += " (registry sync: " + err.Error() + ")"
	} else {
		msg += ", registry synced"
	}
	return msg, nil
}

// --- the peer tool ----------------------------------------------------------------------------

func toolPeer(raw json.RawMessage) (string, []toolImage, error) {
	var a struct {
		Action string          `json:"action"`
		Peer   string          `json:"peer"`
		Via    string          `json:"via"`
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
		Serial string          `json:"serial"`
		Hops   int             `json:"hops"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", nil, err
	}
	switch a.Action {
	case "", "status":
		return peerStatus() + meshStatus(), nil, nil
	case "pair":
		if a.Peer == "" {
			return strings.Join(autoPair(), "\n"), nil, nil
		}
		s, err := tsSelfFn()
		if err != nil {
			return "", nil, err
		}
		p, err := findPeer(s, a.Peer)
		if err != nil {
			return "", nil, err
		}
		if err := pairWith(p); err != nil {
			return "", nil, err
		}
		return p.Name + ": paired", nil, nil
	case "sync":
		return strings.Join(meshTick(), "\n"), nil, nil
	case "adopt":
		if a.Serial == "" && a.Peer != "" {
			if _, e, ok := meshLookup(a.Peer); ok {
				a.Serial = meshAdbSerial(e)
			} else if ip := net.ParseIP(a.Peer); ip != nil {
				a.Serial = net.JoinHostPort(a.Peer, "5555")
			}
		}
		if a.Serial == "" {
			return "", nil, errors.New("adopt needs serial (adb ip:port) or a peer")
		}
		msg, err := meshAdopt(a.Serial, normName(a.Peer))
		return msg, nil, err
	case "allow":
		if a.Peer == "" {
			return "", nil, errors.New("allow needs peer (the device's Tailscale name)")
		}
		return pairAllowAdd(normName(a.Peer))
	case "wake":
		name, e, ok := meshLookup(a.Peer)
		if !ok {
			return "", nil, fmt.Errorf("%s is not in the registry", name)
		}
		msg := meshWake(name, e, a.Hops)
		if a.Hops == 0 {
			for _, d := range meshBackoff {
				meshSleep(d)
				if base, via, _ := meshReachDirect(e); base != "" {
					return msg + "\n" + name + " is up (" + via + ")", nil, nil
				}
			}
			msg += "\n" + name + " not answering yet after ~90 s"
		}
		return msg, nil, nil
	case "relay":
		if a.Peer == "" || a.Via == "" || a.Tool == "" {
			return "", nil, errors.New("relay needs peer, via and tool")
		}
		return meshViaRelay(normName(a.Via), normName(a.Peer), a.Tool, a.Args)
	case "call":
		if a.Peer == "" || a.Tool == "" {
			return "", nil, errors.New("call needs peer and tool")
		}
		if a.Tool == "peer" && a.Hops == 0 {
			return "", nil, errors.New("call a tool on the peer itself, not its peer tool (relay does that)")
		}
		if _, _, ok := meshLookup(a.Peer); !ok && a.Hops == 0 {
			// a computer of the tailnet that isn't in the registry yet: pair it first
			if s, err := tsSelfFn(); err == nil {
				if p, err := findPeer(s, a.Peer); err == nil {
					if e, ok := loadPeers()[p.Name]; !ok || e.Token == "" {
						if err := pairWith(p); err != nil {
							return "", nil, err
						}
					}
					a.Peer = p.Name
				}
			}
		}
		text, imgs, via, err := meshCall(a.Peer, a.Tool, a.Args, a.Hops)
		if err == nil && via != "" && via != "lan" && via != "tailscale" && a.Hops == 0 {
			text += "\n(reached via " + via + ")"
		}
		return text, imgs, err
	}
	return "", nil, fmt.Errorf("unknown peer action %q", a.Action)
}

func pairAllowAdd(name string) (string, []toolImage, error) {
	var c map[string]any
	if b, err := os.ReadFile(pairingCfgPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if c == nil {
		c = map[string]any{}
	}
	list := pairAllowlist()
	for _, n := range list {
		if n == name {
			return name + " is already on the allowlist", nil, nil
		}
	}
	c["allow"] = append(list, name)
	os.MkdirAll(filepath.Dir(pairingCfgPath()), 0o755)
	if err := writeJSONAtomic(pairingCfgPath(), c); err != nil {
		return "", nil, err
	}
	pairLog("allowlist: added %s", name)
	return name + " added to the allowlist", nil, nil
}

// meshStatus: every registry node with each route probed at once (tokens never shown).
func meshStatus() string {
	m := loadPeers()
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	lines := make([]string, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Add(1)
		go func(i int, n string) {
			defer wg.Done()
			e := m[n]
			var rs []string
			for _, r := range meshRoutes(e) {
				st := "up"
				if why := meshProbeFn(r.Base, r.Timeout); why != "" {
					st = why
				}
				rs = append(rs, r.Via+" "+st)
			}
			extra := ""
			if e.Kind == "android" {
				extra = ", adb " + orDash(meshAdbSerial(e))
			}
			if e.MAC != "" {
				extra += ", wake ok"
			}
			lines[i] = fmt.Sprintf("  %s (%s): %s%s; link from %s", n, orStr(e.Kind, "computer"), strings.Join(rs, ", "), extra, effTime(e).Format("2006-01-02 15:04"))
		}(i, n)
	}
	wg.Wait()
	name, _, _ := meshSelf()
	return fmt.Sprintf("registry on %s (primary %s), %d nodes:\n%s\n", name, orDash(meshPrimary()), len(names), strings.Join(lines, "\n"))
}
