package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLinkHandler(t *testing.T) {
	appHome = t.TempDir()
	os.MkdirAll(appHome, 0o755)
	c := linkConfig{Token: "secret", Port: 1}
	srv := httptest.NewServer(linkHandler(c))
	defer srv.Close()
	post := func(path, tok, body string) (int, string) {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 4096)
		n, _ := resp.Body.Read(b)
		return resp.StatusCode, string(b[:n])
	}
	if code, _ := post("/mcp", "wrong", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != 401 {
		t.Fatalf("bad token should be 401, got %d", code)
	}
	if code, body := post("/mcp", "secret", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != 200 || !strings.Contains(body, `"result":{}`) {
		t.Fatalf("ping: %d %s", code, body)
	}
	if _, body := post("/mcp", "secret", `{"jsonrpc":`); !strings.Contains(body, "-32700") {
		t.Fatalf("parse error expected, got %s", body)
	}
	if code, _ := post("/mcp", "secret", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != 202 {
		t.Fatalf("notification should be 202, got %d", code)
	}
	code, body := post("/phone/report", "secret", `{"LanIP":"192.168.1.50","TailscaleIP":"100.1.2.3","AdbSerial":"192.168.1.50:4242"}`)
	if code != 200 {
		t.Fatalf("report: %d", code)
	}
	var rep struct {
		Routes map[string]*string `json:"routes"`
	}
	if err := json.Unmarshal([]byte(body), &rep); err != nil {
		t.Fatalf("report reply is not JSON: %s", body)
	}
	for _, k := range []string{"tunnel_url", "public_host", "tailscale_ip", "lan_ip"} {
		if v, ok := rep.Routes[k]; !ok || v == nil {
			t.Fatalf("routes.%s missing in %s", k, body)
		}
	}
	if *rep.Routes["tunnel_url"] != "" || *rep.Routes["public_host"] != "" {
		t.Fatalf("unset routes must be empty strings: %s", body)
	}
	if code, _ := post("/phone/report", "wrong", `{}`); code != 401 {
		t.Fatalf("report without token should be 401, got %d", code)
	}
	if pc := loadPhoneCfg(); pc.LanIP != "192.168.1.50" || pc.TSIP != "100.1.2.3" || pc.AdbSerial != "192.168.1.50:4242" {
		t.Fatalf("report not stored: %+v", pc)
	}
	// AdbPort (service.adb.tls.port) replaces the stale ip:port with the live Wireless-debugging port.
	if code, _ := post("/phone/report", "secret", `{"LanIP":"192.168.1.50","AdbPort":33469}`); code != 200 {
		t.Fatalf("report with AdbPort: %d", code)
	}
	if pc := loadPhoneCfg(); pc.AdbSerial != "192.168.1.50:33469" || pc.LastReport.IsZero() {
		t.Fatalf("AdbPort not applied: %+v", pc)
	}
	if code, _ := post("/phone/report", "secret", `{"AdbPort":"44555"}`); code != 200 {
		t.Fatalf("report with string AdbPort: %d", code)
	}
	if pc := loadPhoneCfg(); pc.AdbSerial != "192.168.1.50:44555" {
		t.Fatalf("string AdbPort not applied: %+v", pc)
	}
	// loadPhoneCfg must reflect phone.json as it is on disk, not a copy from process start.
	fresh := loadPhoneCfg()
	fresh.AdbSerial = "10.0.0.9:1234"
	if err := writeJSONAtomic(phoneCfgPath(), fresh); err != nil {
		t.Fatal(err)
	}
	if pc := loadPhoneCfg(); pc.AdbSerial != "10.0.0.9:1234" {
		t.Fatalf("loadPhoneCfg served a stale copy: %+v", pc)
	}
	resp, _ := http.Get(srv.URL + "/health")
	if resp.StatusCode != 200 {
		t.Fatal("health")
	}
}

func TestConnectorRoute(t *testing.T) {
	appHome = t.TempDir()
	mcpFails = &failLimiter{fails: map[string][]time.Time{}}
	if err := writeJSONAtomic(linkCfgPath(), linkConfig{Token: "phone-secret", ConnectorToken: "conn-secret"}); err != nil {
		t.Fatal(err)
	}
	c := loadLinkCfg()
	if c.ConnectorToken != "conn-secret" || c.Token != "phone-secret" {
		t.Fatalf("link.json round trip: %+v", c)
	}
	srv := httptest.NewServer(linkHandler(c))
	defer srv.Close()
	do := func(method, path, body string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b := make([]byte, 1<<16)
		n, _ := resp.Body.Read(b)
		return resp, string(b[:n])
	}
	good := "/c/conn-secret/mcp"
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"claude.ai","version":"1"}}}`

	// initialize / tools/list / tools/call over plain JSON, no headers at all.
	resp, body := do("POST", good, init, nil)
	if resp.StatusCode != 200 || !strings.Contains(body, `"serverInfo"`) || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("initialize: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	if resp, body = do("POST", good, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil); resp.StatusCode != 200 || !strings.Contains(body, `"name":"scan"`) {
		t.Fatalf("tools/list: %d %s", resp.StatusCode, body)
	}
	call := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`
	if resp, body = do("POST", good, call, nil); resp.StatusCode != 200 || !strings.Contains(body, `"id":3`) {
		t.Fatalf("tools/call: %d %s", resp.StatusCode, body)
	}
	// Notifications get 202 (streamable HTTP), GET gets 405 with Allow: POST.
	if resp, _ = do("POST", good, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); resp.StatusCode != 202 {
		t.Fatalf("notification should be 202, got %d", resp.StatusCode)
	}
	if resp, _ = do("GET", good, "", nil); resp.StatusCode != 405 || resp.Header.Get("Allow") != "POST" {
		t.Fatalf("GET should be 405 + Allow: POST, got %d %q", resp.StatusCode, resp.Header.Get("Allow"))
	}
	// Wrong path: 401, and after enough tries the IP is rate limited.
	for i := 0; i < 20; i++ {
		if resp, _ = do("POST", "/c/wrong-token/mcp", init, nil); resp.StatusCode != 401 {
			t.Fatalf("wrong token should be 401, got %d", resp.StatusCode)
		}
	}
	if resp, _ = do("POST", "/c/wrong-token/mcp", init, nil); resp.StatusCode != 429 {
		t.Fatalf("after 20 failures expected 429, got %d", resp.StatusCode)
	}
	if resp, _ = do("POST", good, init, nil); resp.StatusCode != 429 {
		t.Fatalf("rate limit must apply to the right path too, got %d", resp.StatusCode)
	}
	mcpFails = &failLimiter{fails: map[string][]time.Time{}}
	// The phone token is not accepted in the path, and the connector token is not a bearer token.
	if resp, _ = do("POST", "/c/phone-secret/mcp", init, nil); resp.StatusCode != 401 {
		t.Fatalf("phone token in the path should be 401, got %d", resp.StatusCode)
	}
	if resp, _ = do("POST", "/mcp", init, map[string]string{"Authorization": "Bearer conn-secret"}); resp.StatusCode != 401 {
		t.Fatalf("connector token as bearer should be 401, got %d", resp.StatusCode)
	}
	// The old bearer /mcp route is unchanged.
	if resp, body = do("POST", "/mcp", init, map[string]string{"Authorization": "Bearer phone-secret"}); resp.StatusCode != 200 || !strings.Contains(body, `"serverInfo"`) {
		t.Fatalf("bearer /mcp: %d %s", resp.StatusCode, body)
	}
	if resp, body = do("POST", "/mcp", init, map[string]string{"X-TokenSaver-Token": "phone-secret"}); resp.StatusCode != 200 || !strings.Contains(body, `"serverInfo"`) {
		t.Fatalf("X-TokenSaver-Token /mcp: %d %s", resp.StatusCode, body)
	}
	// Rotation takes effect without restarting the handler.
	newTok := rotateConnectorToken()
	if newTok == "conn-secret" || len(newTok) != 48 {
		t.Fatalf("rotate: %q", newTok)
	}
	if resp, _ = do("POST", good, init, nil); resp.StatusCode != 401 {
		t.Fatalf("old connector token after rotate should be 401, got %d", resp.StatusCode)
	}
	if resp, _ = do("POST", connectorPath(newTok), init, nil); resp.StatusCode != 200 {
		t.Fatalf("new connector token: %d", resp.StatusCode)
	}
	if loadLinkCfg().Token != "phone-secret" {
		t.Fatal("rotating the connector token must not touch the phone token")
	}
}

func TestConnectorURL(t *testing.T) {
	appHome = t.TempDir()
	if u := connectorURL(); u != "" {
		t.Fatalf("no tunnel: expected empty URL, got %q", u)
	}
	tok := ensureConnectorToken()
	if tok == "" || ensureConnectorToken() != tok {
		t.Fatal("ensureConnectorToken must make one token and keep it")
	}
	saveTunnelState(tunnelState{State: "up", URL: "https://quiet-otter.trycloudflare.com", PID: os.Getpid()})
	if u := connectorURL(); u != "https://quiet-otter.trycloudflare.com/c/"+tok+"/mcp" {
		t.Fatalf("connector URL: %q", u)
	}
	if r := connectorReport(); !strings.Contains(r, "temporary trycloudflare") || !strings.Contains(r, tok) {
		t.Fatalf("report should warn about trycloudflare and show the URL: %s", r)
	}
	saveTunnelState(tunnelState{State: "up", URL: "https://pc.example.com", PID: os.Getpid()})
	if r := connectorReport(); strings.Contains(r, "temporary") || !strings.Contains(r, "https://pc.example.com/c/"+tok+"/mcp") {
		t.Fatalf("named tunnel report: %s", r)
	}
}
