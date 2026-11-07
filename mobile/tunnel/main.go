// tsfunnel: a Tailscale node of its own (tsnet) that publishes the phone's local MCP server on a fixed
// public https://<hostname>.<tailnet>.ts.net URL through Tailscale Funnel.
//
// It runs as a raw static linux/arm64 binary inside the Android app (shipped as libtsfunnel.so).
// The androidbin feature makes netmon, DNS and CA roots work under an Android app UID.
//
// It talks to the app over stdout, one event per line:
//
//	MTS LOGIN <url>   sign-in needed: open the URL (or restart with TS_AUTHKEY set)
//	MTS URL <url>     funnel is up at this public URL
//	MTS ERROR <text>  something failed (the process exits after a fatal one)
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "tailscale.com/feature/androidbin"
	_ "tailscale.com/feature/useproxy"
	"tailscale.com/net/netns"
	"tailscale.com/tsnet"
)

var out = struct {
	sync.Mutex
	last map[string]string
}{last: map[string]string{}}

// emit prints one event line, skipping exact repeats of the last line of the same kind.
func emit(kind, text string) {
	out.Lock()
	defer out.Unlock()
	text = strings.ReplaceAll(text, "\n", " ")
	if out.last[kind] == text {
		return
	}
	out.last[kind] = text
	fmt.Fprintf(os.Stdout, "MTS %s %s\n", kind, text)
}

var loginRe = regexp.MustCompile(`https://login\.tailscale\.com/\S+`)

// loginURL pulls the interactive sign-in URL out of a tsnet user log line.
func loginURL(line string) string {
	return strings.TrimRight(loginRe.FindString(line), ".,;)")
}

// newProxy forwards public requests to the app's local HTTP server, keeping the public Host and marking the
// request as https so the app builds correct OAuth URLs. Headers sent from the internet that claim a forwarded
// origin are dropped by Rewrite before ours are set.
func newProxy(target string) http.Handler {
	t := &url.URL{Scheme: "http", Host: target}
	return withLength(&httputil.ReverseProxy{
		// The app's server (NanoHTTPD) drops idle connections quickly; a fresh one per request avoids stale-conn 502s.
		Transport: &http.Transport{DisableKeepAlives: true, Proxy: nil},
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(t)
			r.Out.Host = r.In.Host
			r.Out.Header.Set("X-Forwarded-Proto", "https")
			r.Out.Header.Set("X-Forwarded-Host", r.In.Host)
			if ip, _, err := net.SplitHostPort(r.In.RemoteAddr); err == nil {
				r.Out.Header.Set("X-Forwarded-For", ip)
			}
		},
		FlushInterval: -1, // stream responses (SSE) as they are written
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "MobileTokenSaver engine not reachable on the phone: "+err.Error(), http.StatusBadGateway)
		},
	})
}

const maxBody = 32 << 20

// withLength buffers request bodies of unknown length (HTTP/2, chunked) so the app's server, which does not
// read chunked bodies, always gets a Content-Length.
func withLength(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody && r.ContentLength < 0 {
			b, err := io.ReadAll(io.LimitReader(r.Body, maxBody+1))
			if err != nil {
				http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
				return
			}
			if len(b) > maxBody {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(b))
			r.ContentLength = int64(len(b))
			r.TransferEncoding = nil
		}
		h.ServeHTTP(w, r)
	})
}

func funnelHint(err error) string {
	return "funnel: " + err.Error() + " — in the Tailscale admin console enable HTTPS certificates (DNS page) and " +
		"allow Funnel for this device (Access controls: nodeAttrs with \"funnel\")"
}

func main() {
	dir := flag.String("dir", "", "state directory (kept across restarts)")
	host := flag.String("hostname", "mobiletokensaver", "Tailscale machine name; the URL becomes https://<name>.<tailnet>.ts.net")
	target := flag.String("target", "127.0.0.1:8765", "local address of the MCP server")
	verbose := flag.Bool("v", false, "print tsnet backend logs to stderr")
	flag.Parse()
	if *dir == "" {
		log.Fatal("-dir is required")
	}
	// Inside an Android app, Tailscale's netns can't read the routing table and pins sockets (SO_BINDTODEVICE)
	// to the wrong interface, so every dial times out. Android already routes app sockets; use plain sockets.
	netns.SetEnabled(false)
	useFallbackDNS()
	go selfTest()
	go hintWhileStuck()

	srv := &tsnet.Server{
		Dir:      *dir,
		Hostname: *host,
		AuthKey:  os.Getenv("TS_AUTHKEY"),
		UserLogf: func(format string, a ...any) {
			line := fmt.Sprintf(format, a...)
			if u := loginURL(line); u != "" {
				emit("LOGIN", u)
				select {
				case gotLogin <- struct{}{}:
				default:
				}
			}
			log.Print(line)
		},
		Logf: func(format string, a ...any) {
			if *verbose {
				log.Printf(format, a...)
			}
		},
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := srv.Up(ctx)
	if err != nil {
		emit("ERROR", "tailscale up: "+err.Error())
		os.Exit(2)
	}
	fqdn := strings.TrimSuffix(st.Self.DNSName, ".")
	if fqdn == "" {
		if d := srv.CertDomains(); len(d) > 0 {
			fqdn = d[0]
		}
	}
	ln, err := srv.ListenFunnel("tcp", ":443")
	if err != nil {
		emit("ERROR", funnelHint(err))
		os.Exit(3)
	}
	select {
	case gotLogin <- struct{}{}:
	default:
	}
	emit("URL", "https://"+fqdn)

	hs := &http.Server{Handler: newProxy(*target), ReadHeaderTimeout: 30 * time.Second}
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		hs.Shutdown(sc)
	}()
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		emit("ERROR", "serve: "+err.Error())
		os.Exit(4)
	}
}

// useFallbackDNS: Android has no /etc/resolv.conf. If nothing else (androiddns) has set up the Go resolver,
// ask public resolvers directly so the control server can be found.
func useFallbackDNS() {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil || net.DefaultResolver.Dial != nil {
		return
	}
	servers := []string{"1.1.1.1:53", "8.8.8.8:53", "[2606:4700:4700::1111]:53"}
	net.DefaultResolver.PreferGo = true
	net.DefaultResolver.Dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		var last error
		for _, s := range servers {
			c, err := d.DialContext(ctx, network, s)
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}
	log.Print("dns: using public resolvers (no resolv.conf, no dnsproxyd)")
}

var gotLogin = make(chan struct{}, 1)

// hintWhileStuck says so when no sign-in link or URL has come after a while, instead of staying silent.
func hintWhileStuck() {
	select {
	case <-gotLogin:
	case <-time.After(45 * time.Second):
		emit("ERROR", "no answer from Tailscale yet (see tunnel log below)")
	}
}

// selfTest logs whether plain internet access works, to tell network blocks from Tailscale problems.
func selfTest() {
	if p := os.Getenv("HTTPS_PROXY"); p != "" {
		log.Printf("selftest: using the app's proxy for internet access")
	}
	for _, a := range []string{"1.1.1.1:443"} {
		start := time.Now()
		c, err := net.DialTimeout("tcp", a, 8*time.Second)
		if err != nil {
			log.Printf("selftest: dial %s failed after %v: %v", a, time.Since(start).Round(time.Millisecond), err)
			continue
		}
		c.Close()
		log.Printf("selftest: dial %s ok (%v)", a, time.Since(start).Round(time.Millisecond))
	}
}
