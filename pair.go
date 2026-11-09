package main

// Peer pairing: the user's own computers get each other's link token over Tailscale with no human
// step. POST /pair on the serve port hands this machine's token to a caller that Tailscale itself
// identifies (whois on the connection's real source address) as a device signed in as this
// tailnet's owner AND named on the allowlist. Anything else is refused: loopback or proxied
// connections (cloudflared, tailscale serve), shared-in or tagged devices, other users, other names.
// The "peer" tool pairs with those computers and runs tools on them; serve pairs on its own too.
// Tokens are kept in peers.json (owner-only) and never written to logs or tool output.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultPairAllow: the devices allowed to pair unless pairing.json says otherwise.
var defaultPairAllow = []string{"ultimateviruscollection", "omegajin-g1619-04", "iris-s24-ultra"}

type pairingConfig struct {
	Allow []string `json:"allow"`
}

func pairingCfgPath() string { return filepath.Join(appHome, "pairing.json") }

// pairAllowlist is pairing.json's allow list, or the default when the file is missing or empty.
func pairAllowlist() []string {
	var c pairingConfig
	if b, err := os.ReadFile(pairingCfgPath()); err == nil {
		if json.Unmarshal(b, &c) == nil && len(c.Allow) > 0 {
			out := make([]string, 0, len(c.Allow))
			for _, n := range c.Allow {
				if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
					out = append(out, n)
				}
			}
			return out
		}
	}
	return defaultPairAllow
}

func pairAllowed(name string) bool {
	name = strings.ToLower(name)
	for _, n := range pairAllowlist() {
		if n == name {
			return true
		}
	}
	return false
}

// --- Tailscale ------------------------------------------------------------------------------------

// tsNode is what Tailscale says about one device.
type tsNode struct {
	Name    string // MagicDNS machine name (first label), lower case; set by the tailnet, not the device
	Tailnet string // MagicDNS suffix, e.g. tail90c428.ts.net
	UserID  int64
	Login   string
	Shared  bool // shared in from another tailnet
	OS      string
	Online  bool
	IPs     []string
}

type tsSelfInfo struct {
	Self  tsNode
	Peers []tsNode
}

func splitMagicDNS(dns string) (name, tailnet string) {
	dns = strings.ToLower(strings.TrimSuffix(dns, "."))
	name, tailnet, _ = strings.Cut(dns, ".")
	return
}

// tsWhoisFn asks the local tailscaled who owns ip (tailscale whois --json). A variable for tests.
var tsWhoisFn = func(ip string) (tsNode, error) {
	exe := tailscaleExe()
	if exe == "" {
		return tsNode{}, errors.New("tailscale not found")
	}
	out, err := runQuiet(8*time.Second, exe, "whois", "--json", ip)
	if err != nil {
		return tsNode{}, fmt.Errorf("tailscale whois: %v", err)
	}
	return parseWhois([]byte(out))
}

func parseWhois(b []byte) (tsNode, error) {
	var w struct {
		Node *struct {
			Name      string
			User      int64
			Sharer    int64
			Addresses []string
			Hostinfo  struct{ OS string }
		}
		UserProfile *struct {
			ID        int64
			LoginName string
		}
	}
	if i := bytes.IndexByte(b, '{'); i > 0 {
		b = b[i:]
	}
	if err := json.Unmarshal(b, &w); err != nil || w.Node == nil || w.UserProfile == nil {
		return tsNode{}, errors.New("tailscale whois: no such device")
	}
	n := tsNode{UserID: w.UserProfile.ID, Login: w.UserProfile.LoginName, Shared: w.Node.Sharer != 0 || w.Node.User != w.UserProfile.ID, OS: strings.ToLower(w.Node.Hostinfo.OS)}
	n.Name, n.Tailnet = splitMagicDNS(w.Node.Name)
	for _, a := range w.Node.Addresses {
		ip, _, _ := strings.Cut(a, "/")
		n.IPs = append(n.IPs, ip)
	}
	return n, nil
}

var (
	tsSelfMu  sync.Mutex
	tsSelfAt  time.Time
	tsSelfVal tsSelfInfo
)

// tsSelfFn is this machine's own Tailscale identity and its peers (tailscale status --json),
// cached for a minute. A variable for tests.
var tsSelfFn = func() (tsSelfInfo, error) {
	tsSelfMu.Lock()
	defer tsSelfMu.Unlock()
	if time.Since(tsSelfAt) < time.Minute && tsSelfVal.Self.Name != "" {
		return tsSelfVal, nil
	}
	exe := tailscaleExe()
	if exe == "" {
		return tsSelfInfo{}, errors.New("tailscale not found")
	}
	out, err := runQuiet(10*time.Second, exe, "status", "--json")
	if err != nil {
		return tsSelfInfo{}, fmt.Errorf("tailscale status: %v", err)
	}
	s, err := parseStatus([]byte(out))
	if err != nil {
		return tsSelfInfo{}, err
	}
	tsSelfVal, tsSelfAt = s, time.Now()
	return s, nil
}

func parseStatus(b []byte) (tsSelfInfo, error) {
	type peer struct {
		DNSName      string
		OS           string
		UserID       int64
		TailscaleIPs []string
		Online       bool
		ShareeNode   bool
	}
	var st struct {
		BackendState string
		Self         *peer
		Peer         map[string]peer
		User         map[string]struct {
			ID        int64
			LoginName string
		}
	}
	if i := bytes.IndexByte(b, '{'); i > 0 {
		b = b[i:]
	}
	if err := json.Unmarshal(b, &st); err != nil || st.Self == nil {
		return tsSelfInfo{}, errors.New("tailscale status: unreadable")
	}
	if st.BackendState != "" && st.BackendState != "Running" {
		return tsSelfInfo{}, fmt.Errorf("tailscale is %s", st.BackendState)
	}
	login := func(id int64) string {
		for _, u := range st.User {
			if u.ID == id {
				return u.LoginName
			}
		}
		return ""
	}
	conv := func(p peer) tsNode {
		n := tsNode{UserID: p.UserID, Login: login(p.UserID), Shared: p.ShareeNode, OS: strings.ToLower(p.OS), Online: p.Online, IPs: p.TailscaleIPs}
		n.Name, n.Tailnet = splitMagicDNS(p.DNSName)
		return n
	}
	out := tsSelfInfo{Self: conv(*st.Self)}
	out.Self.Online = true
	for _, p := range st.Peer {
		out.Peers = append(out.Peers, conv(p))
	}
	sort.Slice(out.Peers, func(i, j int) bool { return out.Peers[i].Name < out.Peers[j].Name })
	if out.Self.Name == "" || out.Self.UserID == 0 {
		return tsSelfInfo{}, errors.New("tailscale status: not signed in")
	}
	return out, nil
}

// sameOwner: n is a device of this tailnet signed in as the same user as this machine.
func sameOwner(self, n tsNode) bool {
	return !n.Shared && n.UserID != 0 && n.UserID == self.UserID && n.Tailnet != "" && n.Tailnet == self.Tailnet
}

func ipv4(s string) net.IP {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	return ip.To4()
}

func isTailscaleIP(s string) bool {
	ip := ipv4(s)
	return ip != nil && ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127
}

// localTSIPsFn: this machine's Tailscale addresses. A variable for tests.
var localTSIPsFn = tailscaleIPs

// --- the /pair endpoint ------------------------------------------------------------------------------

type pairReply struct {
	Device      string `json:"device"`
	Token       string `json:"token"`
	Port        int    `json:"port"`
	TailscaleIP string `json:"tailscale_ip"`
}

var proxyHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "Forwarded", "X-Real-Ip", "Cf-Connecting-Ip", "Cf-Ray", "Tailscale-User-Login", "Tailscale-User-Name"}

// pairCaller decides whether r may get the token; it returns the caller's identity or why not.
func pairCaller(r *http.Request) (tsNode, string) {
	for _, h := range proxyHeaders {
		if r.Header.Get(h) != "" {
			return tsNode{}, "came through a proxy (" + h + ")"
		}
	}
	src, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !isTailscaleIP(src) {
		return tsNode{}, "not a direct Tailscale connection"
	}
	la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	dst := ""
	if la != nil {
		dst, _, _ = net.SplitHostPort(la.String())
	}
	onTS := false
	for _, ip := range localTSIPsFn() {
		if ip == dst {
			onTS = true
		}
	}
	if !onTS {
		return tsNode{}, "did not arrive on this machine's Tailscale address"
	}
	self, err := tsSelfFn()
	if err != nil {
		return tsNode{}, "own identity unknown: " + err.Error()
	}
	who, err := tsWhoisFn(src)
	if err != nil {
		return tsNode{}, err.Error()
	}
	if who.Name == "" {
		return who, "unnamed device"
	}
	if !sameOwner(self.Self, who) {
		return who, "not signed in as this tailnet's owner"
	}
	if !pairAllowed(who.Name) {
		return who, "not on the pairing allowlist"
	}
	return who, ""
}

func handlePair(w http.ResponseWriter, r *http.Request) {
	src := clientIP(r)
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if mcpFails.blocked(src) {
		pairLog("refused %s: too many failures", src)
		http.Error(w, "too many failures", http.StatusTooManyRequests)
		return
	}
	who, why := pairCaller(r)
	if why != "" {
		mcpFails.fail(src)
		pairLog("refused %s (%s as %s): %s", src, orDash(who.Name), orDash(who.Login), why)
		http.Error(w, "pairing refused", http.StatusForbidden)
		return
	}
	lc := loadLinkCfg()
	self, _ := tsSelfFn()
	la, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	dst, _, _ := net.SplitHostPort(la.String())
	pairLog("allowed %s (%s as %s)", who.Name, src, who.Login)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(pairReply{Device: self.Self.Name, Token: lc.Token, Port: lc.Port, TailscaleIP: dst})
}

// pairLog appends to logs/pair.log (kept under 1 MB). Never stdout: the peer tool runs inside the
// stdio MCP server. Callers never pass tokens.
var pairLogMu sync.Mutex

func pairLogPath() string { return filepath.Join(appHome, "logs", "pair.log") }

func pairLog(format string, a ...any) {
	pairLogMu.Lock()
	defer pairLogMu.Unlock()
	p := pairLogPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if fi, err := os.Stat(p); err == nil && fi.Size() > 1<<20 {
		os.Remove(p + ".1")
		os.Rename(p, p+".1")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

// --- stored peers -----------------------------------------------------------------------------------

type peerEntry struct {
	IP     string    `json:"ip"`
	Port   int       `json:"port"`
	Token  string    `json:"token"`
	Paired time.Time `json:"paired"`
}

var peersMu sync.Mutex

func peersPath() string { return filepath.Join(appHome, "peers.json") }

func loadPeers() map[string]peerEntry {
	m := map[string]peerEntry{}
	if b, err := os.ReadFile(peersPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

// savePeer stores one peer's link; the file is readable by its owner only.
func savePeer(name string, e peerEntry) error {
	peersMu.Lock()
	defer peersMu.Unlock()
	m := loadPeers()
	m[name] = e
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

// --- client side ----------------------------------------------------------------------------------

var peerHTTP = &http.Client{Timeout: 120 * time.Second}

// pairTargets: allowlisted computers of the same owner other than this one (phones have no /pair).
func pairTargets(s tsSelfInfo) []tsNode {
	var out []tsNode
	for _, p := range s.Peers {
		if p.Name == s.Self.Name || !sameOwner(s.Self, p) || !pairAllowed(p.Name) || p.OS == "android" || p.OS == "ios" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func findPeer(s tsSelfInfo, name string) (tsNode, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range s.Peers {
		if p.Name == name || (len(p.IPs) > 0 && p.IPs[0] == name) {
			if !sameOwner(s.Self, p) {
				return p, fmt.Errorf("%s is not signed in as this tailnet's owner", p.Name)
			}
			if !pairAllowed(p.Name) {
				return p, fmt.Errorf("%s is not on the pairing allowlist (%s)", p.Name, pairingCfgPath())
			}
			return p, nil
		}
	}
	return tsNode{}, fmt.Errorf("no device %q on the tailnet", name)
}

func peerIP(p tsNode) string {
	for _, ip := range p.IPs {
		if isTailscaleIP(ip) {
			return ip
		}
	}
	return ""
}

// pairWith gets p's token through its /pair endpoint and stores it.
func pairWith(p tsNode) error {
	ip := peerIP(p)
	if ip == "" {
		return fmt.Errorf("%s has no Tailscale IPv4 address", p.Name)
	}
	port := loadLinkCfg().Port
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://%s:%d/pair", ip, port), nil)
	resp, err := peerHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s unreachable: %v", p.Name, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
	case 404:
		return fmt.Errorf("%s runs a TokenSaver without pairing (update it)", p.Name)
	case 403:
		return fmt.Errorf("%s refused pairing (its serve log says why)", p.Name)
	default:
		return fmt.Errorf("%s: pairing failed (HTTP %d)", p.Name, resp.StatusCode)
	}
	var rep pairReply
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&rep); err != nil || rep.Token == "" {
		return fmt.Errorf("%s: bad pairing reply", p.Name)
	}
	if rep.Device != "" && rep.Device != p.Name {
		return fmt.Errorf("%s answered as %s", p.Name, rep.Device)
	}
	if rep.Port == 0 {
		rep.Port = port
	}
	pairLog("got the link of %s (%s)", p.Name, ip)
	return savePeer(p.Name, peerEntry{IP: ip, Port: rep.Port, Token: rep.Token, Paired: time.Now()})
}

// mcpHTTPCall sends one MCP tools/call to base/mcp with a bearer token; code is the HTTP status.
func mcpHTTPCall(client *http.Client, base, token, tool string, args json.RawMessage) (text string, imgs []toolImage, code int, err error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest("POST", base+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == 401 {
		return "", nil, 401, errors.New("token rejected")
	}
	var r struct {
		Result struct {
			Content []struct {
				Type, Text, Data, MimeType string
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
		Error *struct{ Message string } `json:"error"`
	}
	if err := json.Unmarshal(rb, &r); err != nil {
		return "", nil, resp.StatusCode, fmt.Errorf("bad reply (%d): %.200s", resp.StatusCode, rb)
	}
	if r.Error != nil {
		return "", nil, resp.StatusCode, errors.New(r.Error.Message)
	}
	var sb strings.Builder
	for _, ct := range r.Result.Content {
		switch ct.Type {
		case "text":
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(ct.Text)
		case "image":
			if b, err := base64.StdEncoding.DecodeString(ct.Data); err == nil {
				imgs = append(imgs, toolImage{data: b, mime: ct.MimeType})
			}
		}
	}
	if r.Result.IsError {
		return sb.String(), imgs, resp.StatusCode, errors.New(sb.String())
	}
	return sb.String(), imgs, resp.StatusCode, nil
}

// peerPing reports whether the stored token still works (true) or was rejected (false).
func peerPing(e peerEntry) (ok bool, err error) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", fmt.Sprintf("http://%s:%d/mcp", e.IP, e.Port), bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+e.Token)
	resp, err := peerHTTP.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == 200, nil
}

// peerCall runs tool on the peer, pairing first when there is no token or it was rejected.
func peerCall(p tsNode, tool string, args json.RawMessage) (string, []toolImage, error) {
	e, ok := loadPeers()[p.Name]
	if !ok || e.Token == "" {
		if err := pairWith(p); err != nil {
			return "", nil, err
		}
		e = loadPeers()[p.Name]
	}
	if ip := peerIP(p); ip != "" {
		e.IP = ip
	}
	base := fmt.Sprintf("http://%s:%d", e.IP, e.Port)
	text, imgs, code, err := mcpHTTPCall(peerHTTP, base, e.Token, tool, args)
	if code == 401 {
		if perr := pairWith(p); perr != nil {
			return "", nil, perr
		}
		e = loadPeers()[p.Name]
		text, imgs, _, err = mcpHTTPCall(peerHTTP, base, e.Token, tool, args)
	}
	if err != nil && code == 0 {
		return "", nil, fmt.Errorf("%s unreachable: %v", p.Name, err)
	}
	return text, imgs, err
}

// autoPair pairs with every allowlisted computer that is online and has no working token yet.
// It returns one line per computer, never a token.
func autoPair() []string {
	s, err := tsSelfFn()
	if err != nil {
		return []string{err.Error()}
	}
	var out []string
	peers := loadPeers()
	for _, p := range pairTargets(s) {
		if !p.Online {
			out = append(out, p.Name+": offline")
			continue
		}
		if e, ok := peers[p.Name]; ok && e.Token != "" {
			if good, err := peerPing(e); good {
				out = append(out, p.Name+": paired")
				continue
			} else if err != nil {
				out = append(out, p.Name+": unreachable")
				continue
			}
		}
		if err := pairWith(p); err != nil {
			pairLog("%v", err)
			out = append(out, p.Name+": "+err.Error())
		} else {
			out = append(out, p.Name+": paired now")
		}
	}
	return out
}

// peerAutoPairLoop runs in serve: shortly after start, then every 10 minutes.
func peerAutoPairLoop(ctx context.Context) {
	wait := 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		autoPair()
		wait = 10 * time.Minute
	}
}

// --- the peer tool ------------------------------------------------------------------------------------

func toolPeer(raw json.RawMessage) (string, []toolImage, error) {
	var a struct {
		Action string          `json:"action"`
		Peer   string          `json:"peer"`
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", nil, err
	}
	switch a.Action {
	case "", "status":
		return peerStatus(), nil, nil
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
	case "call":
		if a.Peer == "" || a.Tool == "" {
			return "", nil, errors.New("call needs peer and tool")
		}
		if a.Tool == "peer" {
			return "", nil, errors.New("call a tool on the peer itself, not its peer tool")
		}
		s, err := tsSelfFn()
		if err != nil {
			return "", nil, err
		}
		p, err := findPeer(s, a.Peer)
		if err != nil {
			return "", nil, err
		}
		return peerCall(p, a.Tool, a.Args)
	}
	return "", nil, fmt.Errorf("unknown peer action %q", a.Action)
}

func peerStatus() string {
	var b strings.Builder
	s, err := tsSelfFn()
	if err != nil {
		return "Tailscale: " + err.Error()
	}
	fmt.Fprintf(&b, "this device: %s (%s, owner %s)\nallowlist: %s\n", s.Self.Name, strings.Join(s.Self.IPs, " "), orDash(s.Self.Login), strings.Join(pairAllowlist(), ", "))
	peers := loadPeers()
	targets := pairTargets(s)
	if len(targets) == 0 {
		b.WriteString("no other allowlisted computers on the tailnet\n")
	}
	for _, p := range targets {
		state := "offline"
		if p.Online {
			state = "online"
		}
		paired := "not paired"
		if e, ok := peers[p.Name]; ok && e.Token != "" {
			paired = "paired " + e.Paired.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&b, "  %s %s (%s) %s, %s\n", p.Name, peerIP(p), p.OS, state, paired)
	}
	return b.String()
}
