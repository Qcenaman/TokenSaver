package main

import (
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
	if code, _ := post("/phone/report", "secret", `{"LanIP":"192.168.1.50","TailscaleIP":"100.1.2.3","AdbSerial":"192.168.1.50:4242"}`); code != 200 {
		t.Fatalf("report: %d", code)
	}
	if pc := loadPhoneCfg(); pc.LanIP != "192.168.1.50" || pc.TSIP != "100.1.2.3" || pc.AdbSerial != "192.168.1.50:4242" {
		t.Fatalf("report not stored: %+v", pc)
	}
	resp, _ := http.Get(srv.URL + "/health")
	if resp.StatusCode != 200 {
		t.Fatal("health")
	}
}
