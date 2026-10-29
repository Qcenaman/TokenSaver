package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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
	if code, _ := post("/mcp", "secret", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != 204 {
		t.Fatalf("notification should be 204, got %d", code)
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
