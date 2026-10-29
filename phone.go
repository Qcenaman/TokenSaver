package main

// phone: reach the AdbSaver engine on the user's Android phone and pass tool calls through.
// Route order: LAN (Wi-Fi) → Tailscale → adb fallback (basic shell/ui/screen) when the engine is down.
// The phone reaches this PC over lan → tailscale → public_host → cloudflared tunnel (see phoneroutes.go).

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
	"regexp"
	"strings"
	"sync"
	"time"
)

type phoneConfig struct {
	LanIP     string `json:"lan_ip,omitempty"`
	TSIP      string `json:"tailscale_ip,omitempty"`
	Port      int    `json:"port,omitempty"`
	Token     string `json:"token,omitempty"`
	AdbSerial string `json:"adb_serial,omitempty"` // last known ip:port for adb connect
	Package   string `json:"package,omitempty"`
	// filled by the phone's /phone/report
	LastReport  time.Time `json:"last_report,omitempty"`
	LastNetwork string    `json:"last_network,omitempty"`
	LastBattery string    `json:"last_battery,omitempty"`
}

var (
	phoneMu     sync.Mutex
	phoneCfg    phoneConfig
	phoneLoaded bool
	phoneBase   string // last base URL that answered
	phoneBaseAt time.Time
)

func phoneCfgPath() string { return filepath.Join(appHome, "phone.json") }

// loadPhoneCfg re-reads phone.json on every call: serve updates it with each phone report, and the
// MCP server is a separate process, so a copy loaded at start goes stale (last_report, adb_serial).
func loadPhoneCfg() phoneConfig {
	phoneMu.Lock()
	defer phoneMu.Unlock()
	var c phoneConfig
	if b, err := os.ReadFile(phoneCfgPath()); err == nil {
		json.Unmarshal(b, &c)
	} else if phoneLoaded {
		c = phoneCfg
	}
	if c.Port == 0 {
		c.Port = 8765
	}
	if c.Package == "" {
		c.Package = "com.qcenaman.adbsaver"
	}
	phoneCfg, phoneLoaded = c, true
	return c
}

func savePhoneCfg(c phoneConfig) error {
	phoneMu.Lock()
	phoneCfg, phoneLoaded = c, true
	phoneBase = ""
	phoneMu.Unlock()
	return writeJSONAtomic(phoneCfgPath(), c)
}

var phoneHTTP = &http.Client{Timeout: 120 * time.Second}

func phoneHealth(base string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/health", nil)
	resp, err := phoneHTTP.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// phoneReach returns the first base URL that answers, LAN before Tailscale, cached for a minute.
func phoneReach(c phoneConfig, force bool) (string, string) {
	phoneMu.Lock()
	if !force && phoneBase != "" && time.Since(phoneBaseAt) < time.Minute {
		b := phoneBase
		phoneMu.Unlock()
		return b, "cached"
	}
	phoneMu.Unlock()
	cands := []struct{ ip, via string }{{c.LanIP, "lan"}, {c.TSIP, "tailscale"}}
	for _, cand := range cands {
		if cand.ip == "" {
			continue
		}
		base := fmt.Sprintf("http://%s:%d", cand.ip, c.Port)
		if phoneHealth(base, 2500*time.Millisecond) {
			phoneMu.Lock()
			phoneBase, phoneBaseAt = base, time.Now()
			phoneMu.Unlock()
			return base, cand.via
		}
	}
	return "", ""
}

// phoneCall sends one MCP tools/call to the engine and returns text + images.
func phoneCall(c phoneConfig, base, tool string, args json.RawMessage) (string, []toolImage, error) {
	if c.Token == "" {
		return "", nil, errors.New("phone token unknown: run phone pair while the phone is on the LAN (or set it with phone config)")
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	req, _ := http.NewRequest("POST", base+"/mcp", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := phoneHTTP.Do(req)
	if err != nil {
		phoneMu.Lock()
		phoneBase = ""
		phoneMu.Unlock()
		return "", nil, fmt.Errorf("phone unreachable: %v", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode == 401 {
		return "", nil, errors.New("phone rejected the token: run phone pair again")
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
		return "", nil, fmt.Errorf("phone: bad reply (%d): %.200s", resp.StatusCode, rb)
	}
	if r.Error != nil {
		return "", nil, errors.New("phone: " + r.Error.Message)
	}
	var sb strings.Builder
	var imgs []toolImage
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
		return sb.String(), imgs, errors.New(sb.String())
	}
	return sb.String(), imgs, nil
}

// --- adb side -----------------------------------------------------------------------------

func adbExe() string {
	if p, err := lookPath("adb"); err == nil {
		return p
	}
	return "adb"
}

var adbSerialRe = regexp.MustCompile(`(?m)^(\S+)\s+device\b`)

// adbSerial returns a serial adb currently lists as online (prefer ip:port), connecting via mdns or the last ip:port if none.
func adbSerial(c phoneConfig) (string, error) {
	out, _ := runQuiet(8*time.Second, adbExe(), "devices")
	var ipSerial, any string
	for _, m := range adbSerialRe.FindAllStringSubmatch(out, -1) {
		s := m[1]
		if any == "" {
			any = s
		}
		if strings.Contains(s, ":") && ipSerial == "" {
			ipSerial = s
		}
	}
	if ipSerial != "" {
		return ipSerial, nil
	}
	if any != "" {
		return any, nil
	}
	// Try to connect: the address the phone last reported (its live Wireless-debugging port) first,
	// then mDNS, whose list keeps stale _adb-tls-connect entries from earlier sessions.
	tried := map[string]bool{}
	connect := func(addr string) bool {
		if addr == "" || tried[addr] {
			return false
		}
		tried[addr] = true
		o, _ := runQuiet(10*time.Second, adbExe(), "connect", addr)
		return strings.Contains(o, "connected") && !strings.Contains(o, "cannot") && !strings.Contains(o, "failed")
	}
	if connect(c.AdbSerial) {
		return c.AdbSerial, nil
	}
	if out, _ := runQuiet(6*time.Second, adbExe(), "mdns", "services"); out != "" {
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "_adb-tls-connect") {
				f := strings.Fields(line)
				if len(f) >= 3 && connect(f[len(f)-1]) {
					return f[len(f)-1], nil
				}
			}
		}
	}
	return "", errors.New("no phone reachable over adb (not on the LAN, or wireless debugging off)")
}

func adbShell(serial string, timeout time.Duration, cmd string) (string, error) {
	return runQuiet(timeout, adbExe(), "-s", serial, "shell", cmd)
}

// --- tool ---------------------------------------------------------------------------------

func toolPhone(raw json.RawMessage) (string, []toolImage, error) {
	var a struct {
		Action  string          `json:"action"`
		Tool    string          `json:"tool"`
		Args    json.RawMessage `json:"args"`
		Command string          `json:"command"`
		LanIP   string          `json:"lan_ip"`
		TSIP    string          `json:"tailscale_ip"`
		Token   string          `json:"token"`
		Port    int             `json:"port"`
		Force   bool            `json:"force_adb"`
		Mode    string          `json:"mode"`        // tunnel: start|stop|status; public: setup|set
		Public  string          `json:"public_host"` // public: DDNS name / IP (optional :port); "" with set clears it
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", nil, err
	}
	c := loadPhoneCfg()
	switch strings.ToLower(a.Action) {
	case "status":
		return phoneStatus(c), nil, nil
	case "config":
		if a.LanIP != "" {
			c.LanIP = a.LanIP
		}
		if a.TSIP != "" {
			c.TSIP = a.TSIP
		}
		if a.Token != "" {
			c.Token = a.Token
		}
		if a.Port > 0 {
			c.Port = a.Port
		}
		if err := savePhoneCfg(c); err != nil {
			return "", nil, err
		}
		return phoneStatus(c), nil, nil
	case "pair":
		return phonePair(c)
	case "tunnel":
		return phoneTunnel(a.Mode), nil, nil
	case "public":
		if strings.ToLower(a.Mode) == "set" {
			lc := loadLinkCfg()
			lc.PublicHost = a.Public
			writeJSONAtomic(linkCfgPath(), lc)
			msg := "public_host: " + orDash(lc.PublicHost)
			if err := pushRoutesToPhone(c, nil); err == nil {
				msg += "\nroutes pushed to the phone"
			}
			return msg, nil, nil
		}
		return phonePublicSetup(a.Public), nil, nil
	case "push_routes", "push-routes":
		if err := pushRoutesToPhone(c, nil); err != nil {
			return "", nil, err
		}
		return "routes pushed to the phone\n" + pcRoutesStatus(), nil, nil
	case "adb":
		serial, err := adbSerial(c)
		if err != nil {
			return "", nil, err
		}
		if a.Command == "" {
			return "", nil, errors.New("command required")
		}
		out, err := adbShell(serial, 5*time.Minute, a.Command)
		if err != nil && out == "" {
			return "", nil, err
		}
		return out, nil, nil
	case "", "call":
	default:
		return "", nil, fmt.Errorf("unknown action %q", a.Action)
	}
	if a.Tool == "" {
		return "", nil, errors.New("tool required (one of the phone engine's tools: find read write scan run run_job job ui screen apps notifications system web local_ai saved)")
	}
	if !a.Force {
		if base, _ := phoneReach(c, false); base != "" {
			return phoneCall(c, base, a.Tool, a.Args)
		}
	}
	return phoneAdbFallback(c, a.Tool, a.Args)
}

func phoneStatus(c phoneConfig) string {
	var sb strings.Builder
	base, via := phoneReach(c, true)
	if base != "" {
		fmt.Fprintf(&sb, "engine: reachable via %s (%s)\n", via, base)
	} else {
		sb.WriteString("engine: unreachable (lan=" + orDash(c.LanIP) + " tailscale=" + orDash(c.TSIP) + ")\n")
	}
	if c.Token == "" {
		sb.WriteString("token: missing — run phone pair with the phone on the LAN\n")
	} else {
		sb.WriteString("token: set\n")
	}
	if s, err := adbSerial(c); err == nil {
		sb.WriteString("adb: " + s + "\n")
	} else {
		sb.WriteString("adb: none\n")
	}
	sb.WriteString(pcRoutesStatus())
	return strings.TrimSpace(sb.String())
}

// phoneTunnel: start/stop flip phone.tunnel in link.json (serve picks it up within 10 s); status reads tunnel.json.
func phoneTunnel(mode string) string {
	lc := loadLinkCfg()
	switch strings.ToLower(mode) {
	case "start", "on":
		if cloudflaredExe() == "" {
			return "cloudflared is not installed. Install it with:\n  " + cloudflaredInstall + "\nthen run phone tunnel start again."
		}
		lc.Tunnel = "cloudflared"
		writeJSONAtomic(linkCfgPath(), lc)
		if !phoneHealth(fmt.Sprintf("http://127.0.0.1:%d", lc.Port), time.Second) {
			return "tunnel enabled, but tokensaver serve is not running (tokensaver install-link registers it at sign-in)."
		}
		for i := 0; i < 40; i++ {
			time.Sleep(time.Second)
			if st := loadTunnelState(); st.State == "up" || st.State == "failed" || st.State == "missing" {
				break
			}
		}
	case "stop", "off":
		lc.Tunnel = "off"
		writeJSONAtomic(linkCfgPath(), lc)
		for i := 0; i < 15; i++ {
			if loadTunnelState().State == "off" {
				break
			}
			time.Sleep(time.Second)
		}
	case "", "status":
	default:
		return "mode must be start, stop or status"
	}
	st := loadTunnelState()
	s := "tunnel: " + st.State
	if st.URL != "" {
		s += " " + st.URL
	}
	if st.Error != "" {
		s += " — " + st.Error
	}
	if lc.TunnelHostname != "" {
		s += "\nnamed tunnel: " + lc.TunnelHostname
	}
	return s
}

// phonePair reads the engine's token over adb (the phone must be on the LAN), records the LAN and
// Tailscale addresses, and checks the engine answers.
func phonePair(c phoneConfig) (string, []toolImage, error) {
	serial, err := adbSerial(c)
	if err != nil {
		return "", nil, err
	}
	tok, err := adbShell(serial, 15*time.Second, "run-as "+c.Package+" cat files/token")
	tok = strings.TrimSpace(tok)
	if err != nil || len(tok) < 16 || strings.Contains(tok, " ") {
		return "", nil, fmt.Errorf("could not read the token from the app (%q): is AdbSaver installed and started once?", tok)
	}
	c.Token = tok
	if strings.Contains(serial, ":") {
		c.AdbSerial = serial
		c.LanIP = serial[:strings.Index(serial, ":")]
	}
	if ip, _ := adbShell(serial, 10*time.Second, "ip -4 addr show wlan0 | grep -o 'inet [0-9.]*' | cut -d' ' -f2"); strings.Count(strings.TrimSpace(ip), ".") == 3 {
		c.LanIP = strings.TrimSpace(ip)
	}
	if ts := tailscaleAndroidIP(); ts != "" {
		c.TSIP = ts
	}
	if err := savePhoneCfg(c); err != nil {
		return "", nil, err
	}
	msg := "paired"
	if err := pushLinkToPhone(c); err != nil {
		msg += "\nphone could not store the PC link: " + err.Error()
	} else {
		msg += "\nPC link stored on the phone (it can now reach this PC over LAN, Tailscale, public_host or the tunnel, whichever answers)"
	}
	return msg + "\n" + phoneStatus(c), nil, nil
}

// pushLinkToPhone gives the phone everything it needs to reach this PC from anywhere.
func pushLinkToPhone(c phoneConfig) error {
	base, _ := phoneReach(c, true)
	if base == "" {
		return errors.New("engine unreachable")
	}
	lc := loadLinkCfg()
	_, mac := localRouteTo(c.LanIP)
	host, _ := os.Hostname()
	body, _ := json.Marshal(buildLinkJSON(lc, currentRoutes(lc, c.LanIP), mac, host))
	req, _ := http.NewRequest("POST", base+"/link", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := phoneHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// localRouteTo returns the local IPv4 and MAC of the interface that reaches target (for the phone's Wake-on-LAN).
func localRouteTo(target string) (ip, mac string) {
	if target == "" {
		target = "192.168.1.1"
	}
	conn, err := net.Dial("udp", target+":9")
	if err != nil {
		return "", ""
	}
	defer conn.Close()
	local := conn.LocalAddr().(*net.UDPAddr).IP.To4()
	if local == nil {
		return "", ""
	}
	ip = local.String()
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.Equal(local) {
				return ip, ifc.HardwareAddr.String()
			}
		}
	}
	return ip, ""
}

func tailscaleExe() string {
	if p, err := lookPath("tailscale"); err == nil {
		return p
	}
	for _, p := range []string{`C:\Program Files\Tailscale\tailscale.exe`, "/usr/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

var tsLineRe = regexp.MustCompile(`(?m)^(100\.\d+\.\d+\.\d+)\s+\S+\s+\S+\s+android\b`)

func tailscaleAndroidIP() string {
	exe := tailscaleExe()
	if exe == "" {
		return ""
	}
	out, _ := runQuiet(8*time.Second, exe, "status")
	if m := tsLineRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// phoneAdbFallback covers the basics when the engine is down: run, ui (uiautomator), screen, apps.
func phoneAdbFallback(c phoneConfig, tool string, raw json.RawMessage) (string, []toolImage, error) {
	serial, err := adbSerial(c)
	if err != nil {
		return "", nil, fmt.Errorf("phone engine unreachable and %v", err)
	}
	var a map[string]any
	json.Unmarshal(raw, &a)
	str := func(k string) string { v, _ := a[k].(string); return v }
	num := func(k string) int { v, _ := a[k].(float64); return int(v) }
	note := "(adb fallback: engine unreachable)\n"
	switch tool {
	case "run":
		out, err := adbShell(serial, 5*time.Minute, str("command"))
		code := "exit=0"
		if err != nil {
			code = "exit=" + err.Error()
		}
		lines := strings.Split(out, "\n")
		if n := 20; len(lines) > n {
			lines = lines[len(lines)-n:]
		}
		return note + code + "\n-- tail --\n" + strings.Join(lines, "\n"), nil, nil
	case "ui":
		switch str("action") {
		case "", "tree":
			out, _ := adbShell(serial, 20*time.Second, "uiautomator dump /sdcard/.adbsaver_ui.xml >/dev/null 2>&1 && cat /sdcard/.adbsaver_ui.xml")
			return note + uiXMLToTree(out, num("max")), nil, nil
		case "tap":
			_, err := adbShell(serial, 10*time.Second, fmt.Sprintf("input tap %d %d", num("x"), num("y")))
			return note + "ok", nil, err
		case "swipe":
			_, err := adbShell(serial, 10*time.Second, fmt.Sprintf("input swipe %d %d %d %d 250", num("x"), num("y"), num("x2"), num("y2")))
			return note + "ok", nil, err
		case "type":
			_, err := adbShell(serial, 10*time.Second, "input text "+strings.ReplaceAll(strings.ReplaceAll(str("text"), " ", "%s"), "\"", ""))
			return note + "ok", nil, err
		case "key":
			_, err := adbShell(serial, 10*time.Second, "input keyevent "+str("key"))
			return note + "ok", nil, err
		}
		return "", nil, errors.New("ui action not available over adb fallback: " + str("action"))
	case "screen":
		out, err := runQuiet(30*time.Second, adbExe(), "-s", serial, "exec-out", "screencap", "-p")
		if err != nil || len(out) < 100 {
			return "", nil, errors.New("screencap failed over adb")
		}
		return note + "screenshot (png)", []toolImage{{data: []byte(out), mime: "image/png"}}, nil
	case "apps":
		switch str("action") {
		case "", "list":
			out, _ := adbShell(serial, 20*time.Second, "pm list packages -3")
			return note + strings.ReplaceAll(out, "package:", ""), nil, nil
		case "launch":
			out, _ := adbShell(serial, 20*time.Second, "monkey -p "+str("package")+" -c android.intent.category.LAUNCHER 1")
			return note + "launched " + str("package") + "\n" + out, nil, nil
		case "stop":
			adbShell(serial, 20*time.Second, "am force-stop "+str("package"))
			return note + "stopped " + str("package"), nil, nil
		}
	}
	return "", nil, fmt.Errorf("tool %q needs the phone engine; it is unreachable right now", tool)
}

var uiNodeRe = regexp.MustCompile(`<node [^>]*?text="([^"]*)"[^>]*?content-desc="([^"]*)"[^>]*?clickable="(true|false)"[^>]*?bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"`)

// uiXMLToTree turns a uiautomator dump into the engine's compact "label [l,t,r,b]" form.
func uiXMLToTree(xml string, max int) string {
	if max <= 0 {
		max = 200
	}
	var lines []string
	for _, m := range uiNodeRe.FindAllStringSubmatch(xml, -1) {
		label := m[1]
		if label == "" {
			label = m[2]
		}
		if strings.TrimSpace(label) == "" {
			continue
		}
		mark := "  "
		if m[3] == "true" {
			mark = "* "
		}
		lines = append(lines, fmt.Sprintf("%s%s [%s,%s,%s,%s]", mark, label, m[4], m[5], m[6], m[7]))
		if len(lines) >= max {
			break
		}
	}
	if len(lines) == 0 {
		return "(no readable elements; the screen may be animating — try again)"
	}
	return strings.Join(lines, "\n")
}
