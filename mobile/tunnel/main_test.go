package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginURL(t *testing.T) {
	got := loginURL("To start this tsnet server, restart with TS_AUTHKEY set, or go to: https://login.tailscale.com/a/abc123.")
	if got != "https://login.tailscale.com/a/abc123" {
		t.Fatalf("got %q", got)
	}
	if loginURL("AuthLoop: state is Running; done") != "" {
		t.Fatal("no URL expected")
	}
}

func TestProxyMarksHTTPSAndKeepsHost(t *testing.T) {
	var seen *http.Request
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r
		io.WriteString(w, "ok")
	}))
	defer backend.Close()
	addr := strings.TrimPrefix(backend.URL, "http://")

	req := httptest.NewRequest("POST", "http://phone.tail1234.ts.net/mcp", strings.NewReader("{}"))
	req.Header.Set("X-Forwarded-Proto", "http") // spoofed by the client: must not survive
	req.RemoteAddr = net.JoinHostPort("203.0.113.9", "5555")
	rec := httptest.NewRecorder()
	newProxy(addr).ServeHTTP(rec, req)

	if rec.Code != 200 || rec.Body.String() != "ok" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if seen.Host != "phone.tail1234.ts.net" {
		t.Fatalf("host %q", seen.Host)
	}
	if p := seen.Header.Values("X-Forwarded-Proto"); len(p) != 1 || p[0] != "https" {
		t.Fatalf("proto %v", p)
	}
	if seen.Header.Get("X-Forwarded-For") != "203.0.113.9" {
		t.Fatalf("xff %q", seen.Header.Get("X-Forwarded-For"))
	}
	if seen.URL.Path != "/mcp" {
		t.Fatalf("path %q", seen.URL.Path)
	}
}

func TestProxyBackendDown(t *testing.T) {
	rec := httptest.NewRecorder()
	newProxy("127.0.0.1:1").ServeHTTP(rec, httptest.NewRequest("GET", "http://x/health", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestFunnelHint(t *testing.T) {
	if !strings.Contains(funnelHint(errors.New("not allowed")), "nodeAttrs") {
		t.Fatal("hint should point at the ACL")
	}
}

func TestChunkedBodyGetsLength(t *testing.T) {
	var gotLen int64
	var gotTE []string
	var body string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLen, gotTE = r.ContentLength, r.TransferEncoding
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	defer backend.Close()
	req := httptest.NewRequest("POST", "http://x/mcp", io.NopCloser(strings.NewReader(`{"a":1}`)))
	req.ContentLength = -1
	rec := httptest.NewRecorder()
	newProxy(strings.TrimPrefix(backend.URL, "http://")).ServeHTTP(rec, req)
	if gotLen != 7 || len(gotTE) != 0 || body != `{"a":1}` {
		t.Fatalf("len %d te %v body %q", gotLen, gotTE, body)
	}
}
