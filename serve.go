package main

// serve: a token-protected HTTP front for the same MCP tools, on every address of the PC, so the
// phone (AdbSaver) reaches it over the LAN, Tailscale, a forwarded public port or a cloudflared tunnel.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type linkConfig struct {
	Token          string `json:"token"`
	Port           int    `json:"port,omitempty"`
	Tunnel         string `json:"tunnel,omitempty"`          // cloudflared | off; empty = on when cloudflared is installed
	TunnelHostname string `json:"tunnel_hostname,omitempty"` // named tunnel: stable hostname
	TunnelCreds    string `json:"tunnel_credentials,omitempty"`
	PublicHost     string `json:"public_host,omitempty"` // DDNS name or IP (optional :port) forwarded on the router

	// Explicit paths recorded by install-link (in the user's context), because the service runs
	// as SYSTEM, whose PATH and profile folders are not the user's.
	CloudflaredExe  string `json:"cloudflared_exe,omitempty"`
	AdbExe          string `json:"adb_exe,omitempty"`
	AndroidUserHome string `json:"android_user_home,omitempty"` // the user's .android folder (adb keys)
}

func linkCfgPath() string { return filepath.Join(appHome, "link.json") }

func loadLinkCfg() linkConfig {
	var c linkConfig
	if b, err := os.ReadFile(linkCfgPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if c.Port == 0 {
		c.Port = 8766
	}
	if c.Token == "" {
		b := make([]byte, 24)
		rand.Read(b)
		c.Token = hex.EncodeToString(b)
		writeJSONAtomic(linkCfgPath(), c)
	}
	return c
}

// tailscaleIPs lists this machine's Tailscale IPv4 addresses (100.64.0.0/10).
func tailscaleIPs() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
				out = append(out, ip4.String())
			}
		}
	}
	return out
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return r.Header.Get("X-TokenSaver-Token")
}

func linkHandler(c linkConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		ip := clientIP(r)
		if mcpFails.blocked(ip) {
			http.Error(w, "too many failures", http.StatusTooManyRequests)
			return
		}
		if bearer(r) != c.Token {
			mcpFails.fail(ip)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
		var req rpcReq
		if err := json.Unmarshal(body, &req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`)
			return
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			w.WriteHeader(204)
			return
		}
		out, _ := json.Marshal(handleRPC(req))
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	})
	// The phone reports its addresses whenever they change (and every ~15 min); the reply always
	// carries this PC's current routes, which is how the phone learns a new tunnel URL on mobile data.
	mux.HandleFunc("/phone/report", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != c.Token {
			mcpFails.fail(clientIP(r))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var rep struct {
			LanIP, TailscaleIP, AdbSerial, Battery, Network string
			AdbTcp                                          string          // <tailscale-ip>:5555, AdbSaver's fixed classic-TCP adb port
			AdbPort                                         json.RawMessage // service.adb.tls.port: the live Wireless-debugging port
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		json.Unmarshal(body, &rep)
		pc := loadPhoneCfg()
		if rep.LanIP != "" {
			pc.LanIP = rep.LanIP
		}
		if rep.TailscaleIP != "" {
			pc.TSIP = rep.TailscaleIP
		}
		if rep.AdbSerial != "" {
			pc.AdbSerial = rep.AdbSerial
		}
		if rep.AdbTcp != "" {
			pc.AdbTCP = rep.AdbTcp
		}
		if port := strings.Trim(string(rep.AdbPort), `"`); port != "" && port != "0" && port != "null" && pc.LanIP != "" {
			pc.AdbSerial = net.JoinHostPort(pc.LanIP, port)
		}
		pc.LastReport, pc.LastNetwork, pc.LastBattery = time.Now(), rep.Network, rep.Battery
		savePhoneCfg(pc)
		lc := loadLinkCfg()
		out, _ := json.Marshal(map[string]any{"routes": currentRoutes(lc, pc.LanIP)})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	})
	return mux
}

var (
	errServeUpdated   = errors.New("the installed program changed")
	errServeDuplicate = errors.New("another tokensaver serve is running")
)

// serveLink is "tokensaver serve": as a Windows service it runs under the service control
// manager; from a console it runs until Ctrl+C.
func serveLink() {
	if runServeService() {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := serveLinkCtx(ctx, false); err != nil && !errors.Is(err, errServeUpdated) {
		os.Exit(1)
	}
}

// serveLinkCtx runs the HTTP front on all IPv4 addresses and supervises the cloudflared tunnel
// (link.json is re-read every 10 s, so phone tunnel start/stop take effect without a restart).
// It returns nil when ctx ends, errServeUpdated when the installed program was replaced (the
// service then exits with an error so Windows restarts it on the new program), or
// errServeDuplicate when another serve holds the single-instance lock (console only).
func serveLinkCtx(ctx context.Context, asService bool) error {
	openServeLog(!asService)
	serveLog("tokensaver %s serve starting (pid %d, service=%v)", appVersion, os.Getpid(), asService)
	release, err := serveSingleInstance()
	for err != nil {
		if !asService {
			serveLog("%v: %v", errServeDuplicate, err)
			return errServeDuplicate
		}
		serveLog("waiting for the other tokensaver serve to end: %v", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(5 * time.Second):
		}
		release, err = serveSingleInstance()
	}
	defer release()

	c := loadLinkCfg()
	applyServeEnv(c)
	go aiEnsureWatcher()
	srv := &http.Server{Handler: linkHandler(c), ReadHeaderTimeout: 10 * time.Second}
	go keepListening(ctx, srv, c.Port)
	go adbKeepAlive()

	if n := killTunnelOrphans(c.Port); n > 0 {
		serveLog("killed %d leftover cloudflared process(es)", n)
	}
	saveTunnelState(tunnelState{State: "off"})
	var push routePusher
	tun := &tunnelSupervisor{onURL: func(u string) {
		if u != "" {
			serveLog("tunnel %s", u)
		}
		push.newURL(u)
	}}
	shutdown := func() {
		tun.stop()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		srv.Shutdown(sctx)
		cancel()
	}
	watch := newExeWatch()
	var running linkConfig
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		lc := loadLinkCfg()
		if tunnelWanted(lc) {
			if tun.running() && tunnelKey(lc) != tunnelKey(running) {
				tun.restart(lc)
			} else {
				tun.start(lc)
			}
			running = lc
		} else {
			tun.stop()
		}
		select {
		case <-ctx.Done():
			serveLog("stopping")
			shutdown()
			return nil
		case <-tick.C:
			if watch.changed() {
				serveLog("the installed program was replaced; stopping so the new one starts")
				shutdown()
				return errServeUpdated
			}
		}
	}
}

// tunnelKey is what makes a running tunnel outdated when it changes.
func tunnelKey(c linkConfig) string {
	return fmt.Sprint(c.Port, "|", c.TunnelHostname, "|", c.TunnelCreds, "|", c.CloudflaredExe)
}

// keepListening binds the link port and serves until ctx ends; a failed bind is logged with
// its reason (and who holds the port) and retried, never silently given up.
func keepListening(ctx context.Context, srv *http.Server, port int) {
	addr := net.JoinHostPort("0.0.0.0", fmt.Sprint(port))
	wait := time.Second
	for ctx.Err() == nil {
		ln, err := net.Listen("tcp4", addr)
		if err != nil {
			holder := ""
			if pid := listenerPid(port); pid > 0 {
				holder = fmt.Sprintf(" (port held by pid %d)", pid)
			}
			serveLog("cannot listen on %s: %v%s; retrying in %s", addr, err, holder, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, 30*time.Second)
			continue
		}
		wait = time.Second
		serveLog("link listening on %s", addr)
		err = srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		serveLog("link server stopped: %v; listening again", err)
	}
}

// exeWatch notices when the program this serve runs from is replaced (update / install).
type exeWatch struct {
	path     string
	size     int64
	mod      time.Time
	seen     bool // a change was seen at the last check (it must be stable for two checks)
	seenSize int64
	seenMod  time.Time
}

func newExeWatch() *exeWatch {
	p, _ := os.Executable()
	if ip := installPath(); exists(ip) && samePath(p, ip) {
		p = ip
	}
	w := &exeWatch{path: p}
	if fi, err := os.Stat(p); err == nil {
		w.size, w.mod = fi.Size(), fi.ModTime()
	}
	return w
}

func (w *exeWatch) changed() bool {
	if w.path == "" || w.mod.IsZero() {
		return false
	}
	fi, err := os.Stat(w.path)
	if err != nil {
		w.seen = false // mid-swap: the file is briefly missing
		return false
	}
	if fi.Size() == w.size && fi.ModTime().Equal(w.mod) {
		w.seen = false
		return false
	}
	if w.seen && fi.Size() == w.seenSize && fi.ModTime().Equal(w.seenMod) {
		return true
	}
	w.seen, w.seenSize, w.seenMod = true, fi.Size(), fi.ModTime()
	return false
}

// applyServeEnv points adb at the user's own keys (recorded by install-link), so a serve running
// as SYSTEM uses the key the phone already trusts instead of a new one that would need approval.
func applyServeEnv(c linkConfig) {
	if c.AndroidUserHome == "" || !exists(c.AndroidUserHome) {
		return
	}
	os.Setenv("ANDROID_USER_HOME", c.AndroidUserHome)
	if k := filepath.Join(c.AndroidUserHome, "adbkey"); exists(k) {
		os.Setenv("ADB_VENDOR_KEYS", k)
	}
}

// --- route push -------------------------------------------------------------------------------

// routePusher sends each new tunnel URL to the phone and keeps retrying (LAN, then Tailscale)
// until the phone has it or the URL changes again. After a reboot with nobody signed in this is
// how the phone learns the new quick-tunnel URL; the phone's own reports get it too.
type routePusher struct {
	mu  sync.Mutex
	gen int
}

func (p *routePusher) newURL(u string) {
	p.mu.Lock()
	p.gen++
	gen := p.gen
	p.mu.Unlock()
	if u == "" {
		return
	}
	go func() {
		wait := 15 * time.Second
		for attempt := 1; ; attempt++ {
			p.mu.Lock()
			stale := gen != p.gen
			p.mu.Unlock()
			if stale || currentTunnelURL() != u {
				return
			}
			err := pushRoutesToPhone(loadPhoneCfg(), nil)
			if err == nil {
				serveLog("tunnel URL sent to the phone")
				return
			}
			if attempt == 1 || attempt%10 == 0 {
				serveLog("could not send the tunnel URL to the phone yet (retrying; it also gets it at its next report): %v", err)
			}
			time.Sleep(wait)
			wait = min(wait*2, 5*time.Minute)
		}
	}()
}

// --- log --------------------------------------------------------------------------------------

var (
	serveLogMu  sync.Mutex
	serveLogOut io.Writer = io.Discard
	serveEcho             = true
)

func serveLogPath() string { return filepath.Join(appHome, "logs", "serve.log") }

// openServeLog appends to data\logs\serve.log (kept under 2 MB: the old part goes to serve.log.1).
func openServeLog(echo bool) {
	serveLogMu.Lock()
	defer serveLogMu.Unlock()
	serveEcho = echo
	p := serveLogPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if fi, err := os.Stat(p); err == nil && fi.Size() > 2<<20 {
		os.Remove(p + ".1")
		os.Rename(p, p+".1")
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		serveLogOut = f
	}
}

func serveLog(format string, a ...any) {
	line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, a...) + "\n"
	serveLogMu.Lock()
	defer serveLogMu.Unlock()
	serveLogOut.Write([]byte(line))
	if serveEcho {
		os.Stdout.WriteString(line[20:])
	}
}
