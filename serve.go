package main

// serve: an HTTP front for the same MCP tools, bound only to the PC's Tailscale address (and
// localhost), token-protected. Lets the phone (AdbSaver) reach this PC from anywhere.

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
	"sync"
	"time"
)

type linkConfig struct {
	Token string `json:"token"`
	Port  int    `json:"port,omitempty"`
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
		if bearer(r) != c.Token {
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
	// The phone reports its addresses whenever they change, so the phone tool always knows where it is.
	mux.HandleFunc("/phone/report", func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != c.Token {
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
		savePhoneCfg(pc)
		io.WriteString(w, "ok")
	})
	return mux
}

// serveLink runs the HTTP front until the process ends, re-binding when the Tailscale address appears or changes.
func serveLink() {
	c := loadLinkCfg()
	h := linkHandler(c)
	var mu sync.Mutex
	servers := map[string]*http.Server{}
	start := func(ip string) {
		mu.Lock()
		defer mu.Unlock()
		if _, ok := servers[ip]; ok {
			return
		}
		srv := &http.Server{Addr: net.JoinHostPort(ip, fmt.Sprint(c.Port)), Handler: h, ReadHeaderTimeout: 10 * time.Second}
		ln, err := net.Listen("tcp", srv.Addr)
		if err != nil {
			fmt.Println("cannot listen on", srv.Addr+":", err)
			return
		}
		servers[ip] = srv
		fmt.Println("tokensaver link on", srv.Addr)
		go func() {
			srv.Serve(ln)
			mu.Lock()
			delete(servers, ip)
			mu.Unlock()
		}()
	}
	start("127.0.0.1")
	for {
		for _, ip := range tailscaleIPs() {
			start(ip)
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
	fmt.Printf("Link port %d, token %s\nAllow inbound TCP %d from 100.64.0.0/10 only (Tailscale).\n", c.Port, c.Token, c.Port)
}
