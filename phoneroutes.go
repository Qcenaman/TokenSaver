package main

// phoneroutes: the no-Tailscale ways for the phone to reach this PC.
//   tunnel  — cloudflared quick (or named) tunnel supervised by "tokensaver serve"; URL kept in tunnel.json
//   public  — a DDNS name / static IP the user forwards on their own router (UPnP helper)
// Current routes are pushed to the phone (POST /link, no token) and returned in every /phone/report reply.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// tunnelState is what serve writes to tunnel.json so other tokensaver processes can read it.
type tunnelState struct {
	URL     string    `json:"url,omitempty"`
	State   string    `json:"state"` // off | starting | up | missing | failed
	PID     int       `json:"pid,omitempty"`
	Error   string    `json:"error,omitempty"`
	Updated time.Time `json:"updated"`
}

func tunnelStatePath() string { return filepath.Join(appHome, "tunnel.json") }

func loadTunnelState() tunnelState {
	var s tunnelState
	if b, err := os.ReadFile(tunnelStatePath()); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.State == "" {
		s.State = "off"
	}
	// A stale file from a serve that is no longer running must not advertise a dead URL.
	if s.State == "up" && time.Since(s.Updated) > 2*time.Minute && !phoneHealth(fmt.Sprintf("http://127.0.0.1:%d", loadLinkCfg().Port), time.Second) {
		s.State, s.URL = "off", ""
	}
	return s
}

func saveTunnelState(s tunnelState) {
	s.Updated = time.Now()
	writeJSONAtomic(tunnelStatePath(), s)
}

// currentTunnelURL is the tunnel base URL to advertise now, or "".
func currentTunnelURL() string {
	s := loadTunnelState()
	if s.State == "up" {
		return s.URL
	}
	return ""
}

func cloudflaredExe() string {
	if p, err := lookPath("cloudflared"); err == nil {
		return p
	}
	cands := []string{
		`C:\Program Files (x86)\cloudflared\cloudflared.exe`,
		`C:\Program Files\cloudflared\cloudflared.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WinGet", "Links", "cloudflared.exe"),
		"/usr/local/bin/cloudflared", "/usr/bin/cloudflared", "/opt/homebrew/bin/cloudflared",
	}
	for _, p := range cands {
		if exists(p) {
			return p
		}
	}
	return ""
}

const cloudflaredInstall = "winget install --id Cloudflare.cloudflared -e"

var quickTunnelRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// parseCloudflaredURL finds the quick-tunnel URL in a line of cloudflared's stderr ("" if none).
func parseCloudflaredURL(line string) string { return quickTunnelRe.FindString(line) }

// tunnelWanted says whether serve should run a tunnel: phone.tunnel = cloudflared, or unset and cloudflared present.
func tunnelWanted(c linkConfig) bool {
	switch strings.ToLower(c.Tunnel) {
	case "off", "no", "false":
		return false
	case "cloudflared", "on", "yes", "true":
		return true
	}
	return cloudflaredExe() != ""
}

// tunnelSupervisor keeps one cloudflared child running while enabled and records its URL.
type tunnelSupervisor struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	onURL   func(string) // called when the advertised URL changes (new URL or "")
}

func (t *tunnelSupervisor) start(c linkConfig) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		return
	}
	exe := cloudflaredExe()
	if exe == "" {
		saveTunnelState(tunnelState{State: "missing", Error: "cloudflared not installed: " + cloudflaredInstall})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.running = cancel, true
	go t.loop(ctx, exe, c)
}

func (t *tunnelSupervisor) stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.running {
		return
	}
	t.cancel()
	t.running = false
	saveTunnelState(tunnelState{State: "off"})
	if t.onURL != nil {
		t.onURL("")
	}
}

func (t *tunnelSupervisor) loop(ctx context.Context, exe string, c linkConfig) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := t.runOnce(ctx, exe, c)
		if ctx.Err() != nil {
			return
		}
		msg := "cloudflared exited"
		if err != nil {
			msg = err.Error()
		}
		saveTunnelState(tunnelState{State: "failed", Error: msg})
		if t.onURL != nil {
			t.onURL("")
		}
		if time.Since(started) > time.Minute {
			backoff = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 2*time.Minute)
	}
}

// runOnce runs one cloudflared process until it exits; the URL is published as soon as it is known.
func (t *tunnelSupervisor) runOnce(ctx context.Context, exe string, c linkConfig) error {
	local := fmt.Sprintf("http://127.0.0.1:%d", c.Port)
	var args []string
	named := c.TunnelHostname != "" && c.TunnelCreds != "" && exists(c.TunnelCreds)
	if named {
		args = []string{"tunnel", "--no-autoupdate", "--credentials-file", c.TunnelCreds, "--url", local, "run", tunnelIDFromCreds(c.TunnelCreds)}
	} else {
		args = []string{"tunnel", "--no-autoupdate", "--url", local}
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	hideWindow(cmd)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	saveTunnelState(tunnelState{State: "starting", PID: cmd.Process.Pid})
	publish := func(u string) {
		saveTunnelState(tunnelState{State: "up", URL: u, PID: cmd.Process.Pid})
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
			n, err := stderr.Read(buf)
			for _, b := range buf[:n] {
				if b == '\n' {
					if !named {
						if u := parseCloudflaredURL(string(line)); u != "" && u != currentTunnelURL() {
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
	return cmd.Wait()
}

func tunnelIDFromCreds(p string) string {
	var c struct {
		TunnelID string `json:"TunnelID"`
	}
	if b, err := os.ReadFile(p); err == nil {
		json.Unmarshal(b, &c)
	}
	return c.TunnelID
}

// --- routes ---------------------------------------------------------------------------------

// pcRoutes is what the phone needs to reach this PC right now; empty strings mean "none".
type pcRoutes struct {
	TunnelURL   string `json:"tunnel_url"`
	PublicHost  string `json:"public_host"`
	TailscaleIP string `json:"tailscale_ip"`
	LanIP       string `json:"lan_ip"`
}

func currentRoutes(lc linkConfig, phoneLanIP string) pcRoutes {
	r := pcRoutes{TunnelURL: currentTunnelURL(), PublicHost: lc.PublicHost}
	if ips := tailscaleIPs(); len(ips) > 0 {
		r.TailscaleIP = ips[0]
	}
	r.LanIP, _ = localRouteTo(phoneLanIP)
	return r
}

// buildLinkJSON is the link the phone stores (phone pair). public_host and tunnel_url appear only when set.
func buildLinkJSON(lc linkConfig, r pcRoutes, mac, name string) map[string]any {
	m := map[string]any{"token": lc.Token, "port": lc.Port, "lan_ip": r.LanIP, "tailscale_ip": r.TailscaleIP, "mac": mac, "name": name}
	if r.PublicHost != "" {
		m["public_host"] = r.PublicHost
	}
	if r.TunnelURL != "" {
		m["tunnel_url"] = r.TunnelURL
	}
	return m
}

// pushRoutesToPhone re-sends the current routes to the phone without the token (phone must be paired).
// bases: candidate base URLs; empty = probe LAN then Tailscale.
func pushRoutesToPhone(c phoneConfig, bases []string) error {
	if c.Token == "" {
		return errors.New("phone not paired")
	}
	if len(bases) == 0 {
		for _, ip := range []string{c.LanIP, c.TSIP} {
			if ip != "" {
				bases = append(bases, fmt.Sprintf("http://%s:%d", ip, c.Port))
			}
		}
	}
	lc := loadLinkCfg()
	r := currentRoutes(lc, c.LanIP)
	body, _ := json.Marshal(map[string]any{"lan_ip": r.LanIP, "tailscale_ip": r.TailscaleIP, "public_host": r.PublicHost, "tunnel_url": r.TunnelURL, "port": lc.Port})
	var last error = errors.New("phone unreachable on LAN and Tailscale")
	for _, base := range bases {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "POST", base+"/link", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := phoneHTTP.Do(req)
		cancel()
		if err != nil {
			last = err
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			return nil
		}
		if resp.StatusCode == 400 {
			// The phone has no PC link yet (pairing never finished): send the full link, token included.
			_, mac := localRouteTo(c.LanIP)
			host, _ := os.Hostname()
			full, _ := json.Marshal(buildLinkJSON(lc, r, mac, host))
			ctx2, cancel2 := context.WithTimeout(context.Background(), 6*time.Second)
			req2, _ := http.NewRequestWithContext(ctx2, "POST", base+"/link", bytes.NewReader(full))
			req2.Header.Set("Authorization", "Bearer "+c.Token)
			req2.Header.Set("Content-Type", "application/json")
			resp2, err2 := phoneHTTP.Do(req2)
			cancel2()
			if err2 == nil {
				b2, _ := io.ReadAll(io.LimitReader(resp2.Body, 4096))
				resp2.Body.Close()
				if resp2.StatusCode == 200 {
					return nil
				}
				b = b2
				resp.StatusCode = resp2.StatusCode
			}
		}
		last = fmt.Errorf("%s: %d %s", base, resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return last
}

// pcRoutesStatus is the PC-side view for phone status.
func pcRoutesStatus() string {
	lc := loadLinkCfg()
	pc := loadPhoneCfg()
	r := currentRoutes(lc, pc.LanIP)
	ts := loadTunnelState()
	tun := ts.State
	if ts.URL != "" && ts.State == "up" {
		tun = ts.URL
	} else if ts.Error != "" {
		tun += " (" + ts.Error + ")"
	}
	serve := "running"
	if !phoneHealth(fmt.Sprintf("http://127.0.0.1:%d", lc.Port), time.Second) {
		serve = "NOT running (tokensaver serve / install-link)"
	}
	s := fmt.Sprintf("PC link (port %d, serve %s):\n  lan=%s  tailscale=%s  public=%s  tunnel=%s", lc.Port, serve, orDash(r.LanIP), orDash(r.TailscaleIP), orDash(r.PublicHost), tun)
	if !pc.LastReport.IsZero() {
		s += fmt.Sprintf("\nphone's last report: %s ago (lan=%s tailscale=%s network=%s battery=%s)", time.Since(pc.LastReport).Round(time.Second), orDash(pc.LanIP), orDash(pc.TSIP), orDash(pc.LastNetwork), orDash(pc.LastBattery))
	}
	return s
}

// --- 401 rate limit (the tunnel exposes serve to the internet) -------------------------------

type failLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

var mcpFails = &failLimiter{fails: map[string][]time.Time{}}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("Cf-Connecting-Ip"); ip != "" {
		return ip
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// blocked reports whether ip has failed too often (20 in 10 min); fail records one more failure.
func (f *failLimiter) blocked(ip string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	cut := time.Now().Add(-10 * time.Minute)
	keep := f.fails[ip][:0]
	for _, t := range f.fails[ip] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	f.fails[ip] = keep
	return len(keep) >= 20
}

func (f *failLimiter) fail(ip string) {
	f.mu.Lock()
	f.fails[ip] = append(f.fails[ip], time.Now())
	f.mu.Unlock()
}

// --- public route: UPnP + CGNAT check ---------------------------------------------------------

type upnpGateway struct {
	control string // control URL of WANIPConnection / WANPPPConnection
	service string
}

// upnpDiscover finds the router's WAN connection service (SSDP, then the device description).
func upnpDiscover(timeout time.Duration) (*upnpGateway, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	msg := "M-SEARCH * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nMAN: \"ssdp:discover\"\r\nMX: 2\r\nST: urn:schemas-upnp-org:device:InternetGatewayDevice:1\r\n\r\n"
	dst, _ := net.ResolveUDPAddr("udp4", "239.255.255.250:1900")
	conn.WriteTo([]byte(msg), dst)
	conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 4096)
	seen := map[string]bool{}
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		for _, line := range strings.Split(string(buf[:n]), "\r\n") {
			if l := strings.ToLower(line); strings.HasPrefix(l, "location:") {
				loc := strings.TrimSpace(line[9:])
				if seen[loc] {
					continue
				}
				seen[loc] = true
				if g, err := upnpFromDescription(loc); err == nil {
					return g, nil
				}
			}
		}
	}
	return nil, errors.New("no UPnP gateway answered (UPnP off on the router, or no router)")
}

func upnpFromDescription(loc string) (*upnpGateway, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", loc, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	type service struct {
		ServiceType string `xml:"serviceType"`
		ControlURL  string `xml:"controlURL"`
	}
	type device struct {
		Services []service `xml:"serviceList>service"`
		Devices  []device  `xml:"deviceList>device"`
	}
	var root struct {
		URLBase string `xml:"URLBase"`
		Device  device `xml:"device"`
	}
	if err := xml.Unmarshal(b, &root); err != nil {
		return nil, err
	}
	base, _ := url.Parse(loc)
	if root.URLBase != "" {
		if u, err := url.Parse(root.URLBase); err == nil {
			base = u
		}
	}
	var walk func(d device) *upnpGateway
	walk = func(d device) *upnpGateway {
		for _, s := range d.Services {
			if strings.Contains(s.ServiceType, "WANIPConnection") || strings.Contains(s.ServiceType, "WANPPPConnection") {
				cu, err := base.Parse(s.ControlURL)
				if err != nil {
					continue
				}
				return &upnpGateway{control: cu.String(), service: s.ServiceType}
			}
		}
		for _, sub := range d.Devices {
			if g := walk(sub); g != nil {
				return g
			}
		}
		return nil
	}
	if g := walk(root.Device); g != nil {
		return g, nil
	}
	return nil, errors.New("gateway has no WAN connection service")
}

func (g *upnpGateway) soap(action, args string) (string, error) {
	body := `<?xml version="1.0"?><s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body><u:` + action + ` xmlns:u="` + g.service + `">` + args + `</u:` + action + `></s:Body></s:Envelope>`
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", g.control, strings.NewReader(body))
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", `"`+g.service+"#"+action+`"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		if m := regexp.MustCompile(`<errorDescription>([^<]*)`).FindSubmatch(b); m != nil {
			return "", fmt.Errorf("%s: %s", action, m[1])
		}
		return "", fmt.Errorf("%s: HTTP %d", action, resp.StatusCode)
	}
	return string(b), nil
}

func (g *upnpGateway) externalIP() string {
	out, err := g.soap("GetExternalIPAddress", "")
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`<NewExternalIPAddress>([^<]*)`).FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

func (g *upnpGateway) addMapping(proto string, port int, toIP string, desc string) error {
	args := fmt.Sprintf("<NewRemoteHost></NewRemoteHost><NewExternalPort>%d</NewExternalPort><NewProtocol>%s</NewProtocol><NewInternalPort>%d</NewInternalPort><NewInternalClient>%s</NewInternalClient><NewEnabled>1</NewEnabled><NewPortMappingDescription>%s</NewPortMappingDescription><NewLeaseDuration>0</NewLeaseDuration>", port, proto, port, toIP, desc)
	_, err := g.soap("AddPortMapping", args)
	return err
}

func publicIP() string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.ipify.org", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return strings.TrimSpace(string(b))
}

// isCGNAT: the WAN address the router sees is not the public one, or is itself a shared/private range.
func isCGNAT(wan, public string) bool {
	ip := net.ParseIP(wan)
	if ip == nil {
		return public == ""
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	return public != "" && wan != public
}

// phonePublicSetup forwards the link port on the router, opens the firewall for all profiles and
// says whether the ISP uses CGNAT (then only the tunnel can work).
func phonePublicSetup(host string) string {
	lc := loadLinkCfg()
	if host != "" {
		lc.PublicHost = host
		writeJSONAtomic(linkCfgPath(), lc)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "public_host: %s\n", orDash(lc.PublicHost))
	sb.WriteString(firewallAllowLink(lc.Port) + "\n")
	pc := loadPhoneCfg()
	lan, _ := localRouteTo(pc.LanIP)
	pub := publicIP()
	g, err := upnpDiscover(3 * time.Second)
	wan := ""
	if err != nil {
		sb.WriteString("UPnP: " + err.Error() + " — forward TCP " + fmt.Sprint(lc.Port) + " to " + lan + " on the router by hand\n")
	} else {
		wan = g.externalIP()
		if err := g.addMapping("TCP", lc.Port, lan, "TokenSaver Link"); err != nil {
			sb.WriteString("UPnP: port forward failed: " + err.Error() + "\n")
		} else {
			fmt.Fprintf(&sb, "UPnP: TCP %d → %s forwarded\n", lc.Port, lan)
		}
		if err := g.addMapping("UDP", 9, lan, "TokenSaver wake"); err != nil {
			sb.WriteString("UPnP: UDP 9 (wake-on-LAN from outside) not forwarded: " + err.Error() + " — pc wake only works on LAN/Tailscale\n")
		} else {
			sb.WriteString("UPnP: UDP 9 → " + lan + " forwarded (wake from outside may work while the router still remembers this PC)\n")
		}
	}
	fmt.Fprintf(&sb, "public IP: %s   router WAN IP: %s\n", orDash(pub), orDash(wan))
	if pub == "" {
		sb.WriteString("could not read the public IP (offline?)\n")
	} else if isCGNAT(wan, pub) && wan != "" {
		sb.WriteString("CGNAT: the ISP shares the public IP; port forwarding cannot work — use the tunnel route (phone tunnel start)\n")
	} else if wan == "" {
		sb.WriteString("router WAN IP unknown: if " + pub + " is not on the router's WAN page the ISP uses CGNAT and only the tunnel works\n")
	} else {
		sb.WriteString("no CGNAT: point your DDNS name at " + pub + " and set it as public_host\n")
	}
	if lc.PublicHost != "" {
		if err := pushRoutesToPhone(pc, nil); err == nil {
			sb.WriteString("routes pushed to the phone\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// firewallAllowLink adds an inbound rule for the link port on every profile (through the admin helper).
func firewallAllowLink(port int) string {
	if !isWin {
		return "firewall: open TCP " + fmt.Sprint(port) + " inbound yourself"
	}
	out, _ := runQuiet(10*time.Second, "netsh", "advfirewall", "firewall", "show", "rule", "name=TokenSaver Link")
	if strings.Contains(out, "Profiles:") && (strings.Contains(out, "Domain,Private,Public") || strings.Contains(out, "Profiles:                             Any") || regexp.MustCompile(`Profiles:\s+(Any|Domain,Private,Public)`).MatchString(out)) {
		return "firewall: TokenSaver Link rule already allows all profiles"
	}
	cmd := fmt.Sprintf(`netsh advfirewall firewall add rule name="TokenSaver Link" dir=in action=allow protocol=TCP localport=%d profile=any`, port)
	var buf bytes.Buffer
	ep, err := startElevated(elevRequest{Command: cmd, Shell: "cmd"}, &buf)
	if err != nil {
		return "firewall: admin helper unavailable (" + err.Error() + "); run as admin: " + cmd
	}
	<-ep.done
	if ep.err != nil || (ep.code != nil && *ep.code != 0) {
		return "firewall: rule not added: " + strings.TrimSpace(buf.String())
	}
	return fmt.Sprintf("firewall: TCP %d allowed inbound on all profiles", port)
}

// phoneCLI: tokensaver phone status | pair | tunnel start|stop|status | public setup [host] | public set <host> | push-routes
func phoneCLI(args []string) int {
	if len(args) == 0 {
		fmt.Println("usage: tokensaver phone status | pair | tunnel start|stop|status | public setup [host] | public set <host> | push-routes")
		return 2
	}
	a := map[string]any{"action": args[0]}
	if len(args) > 1 {
		a["mode"] = args[1]
	}
	if len(args) > 2 {
		a["public_host"] = args[2]
	}
	raw, _ := json.Marshal(a)
	out, _, err := toolPhone(raw)
	if out != "" {
		fmt.Println(out)
	}
	if err != nil {
		fmt.Println("error:", err)
		return 1
	}
	return 0
}
