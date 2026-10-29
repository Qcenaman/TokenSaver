package main

// serve: a token-protected HTTP front for the same MCP tools, on every address of the PC, so the
// phone (AdbSaver) reaches it over the LAN, Tailscale, a forwarded public port or a cloudflared tunnel.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type linkConfig struct {
	Token          string `json:"token"`
	Port           int    `json:"port,omitempty"`
	Tunnel         string `json:"tunnel,omitempty"`          // cloudflared | off; empty = on when cloudflared is installed
	TunnelHostname string `json:"tunnel_hostname,omitempty"` // named tunnel: stable hostname
	TunnelCreds    string `json:"tunnel_credentials,omitempty"`
	PublicHost     string `json:"public_host,omitempty"` // DDNS name or IP (optional :port) forwarded on the router
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
			http.Error(w, "POST only", 405)
			return
		}
		ip := clientIP(r)
		if mcpFails.blocked(ip) {
			http.Error(w, "too many failures", 429)
			return
		}
		if bearer(r) != c.Token {
			mcpFails.fail(ip)
			http.Error(w, "unauthorized", 401)
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
			http.Error(w, "unauthorized", 401)
			return
		}
		var rep struct {
			LanIP, TailscaleIP, AdbSerial, Battery, Network string
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
		pc.LastReport, pc.LastNetwork, pc.LastBattery = time.Now(), rep.Network, rep.Battery
		savePhoneCfg(pc)
		lc := loadLinkCfg()
		out, _ := json.Marshal(map[string]any{"routes": currentRoutes(lc, pc.LanIP)})
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	})
	return mux
}

// serveLink runs the HTTP front on all addresses until the process ends, and supervises the
// cloudflared tunnel when it is wanted (link.json is re-read every 10 s, so phone tunnel start/stop
// take effect without a restart).
func serveLink() {
	c := loadLinkCfg()
	h := linkHandler(c)
	srv := &http.Server{Addr: net.JoinHostPort("0.0.0.0", fmt.Sprint(c.Port)), Handler: h, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		fmt.Println("cannot listen on", srv.Addr+":", err)
		os.Exit(1)
	}
	fmt.Println("tokensaver link on", srv.Addr)
	go srv.Serve(ln)
	saveTunnelState(tunnelState{State: "off"})
	tun := &tunnelSupervisor{onURL: func(u string) {
		if u == "" {
			return
		}
		fmt.Println("tunnel", u)
		go func() {
			if err := pushRoutesToPhone(loadPhoneCfg(), nil); err != nil {
				fmt.Println("could not push the tunnel URL to the phone (it will get it at its next report):", err)
			}
		}()
	}}
	for {
		lc := loadLinkCfg()
		if tunnelWanted(lc) {
			tun.start(lc)
		} else {
			tun.stop()
		}
		time.Sleep(10 * time.Second)
	}
}

// consoleInstallLink registers "tokensaver serve" to start at sign-in (hidden) and prints the token.
func consoleInstallLink() {
	c := loadLinkCfg()
	exe := installPath()
	if !exists(exe) {
		exe, _ = os.Executable()
	}
	if isWin {
		tr := `"` + exe + `" serve`
		if err := quietCmd("schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", "TokenSaver Link", "/TR", tr, "/RL", "LIMITED").Run(); err != nil {
			fmt.Println("Couldn't create the scheduled task:", err)
		} else {
			fmt.Println(`Registered "TokenSaver Link" to start at sign-in.`)
			quietCmd("schtasks", "/Run", "/TN", "TokenSaver Link").Run()
		}
	} else {
		fmt.Println("Start it with:", exe, "serve")
	}
	fmt.Printf("Link port %d, token %s\n", c.Port, c.Token)
	fmt.Println(firewallAllowLink(c.Port))
	if cloudflaredExe() == "" {
		fmt.Println("For the no-Tailscale route install cloudflared:", cloudflaredInstall)
	}
}
