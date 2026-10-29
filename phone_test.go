package main

import (
	"strings"
	"testing"
)

func TestUIXMLToTree(t *testing.T) {
	xml := `<hierarchy><node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="x" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,0][1440,3120]">` +
		`<node index="0" text="Wireless debugging" resource-id="android:id/title" class="android.widget.TextView" package="x" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[126,1200][900,1280]" />` +
		`<node index="1" text="" resource-id="" class="android.widget.Button" package="x" content-desc="Navigate up" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,165][270,453]" />` +
		`</node></hierarchy>`
	got := uiXMLToTree(xml, 0)
	want := "  Wireless debugging [126,1200,900,1280]\n* Navigate up [0,165,270,453]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !strings.Contains(uiXMLToTree("<hierarchy/>", 0), "no readable elements") {
		t.Fatal("empty dump should say so")
	}
	if n := strings.Count(uiXMLToTree(xml, 1), "\n"); n != 0 {
		t.Fatalf("max=1 should give one line, got %d newlines", n)
	}
}

func TestTailscaleAndroidLine(t *testing.T) {
	out := "100.109.168.97  pc   user@  windows  -\n100.84.75.31    s24  user@  android  active; direct 1.2.3.4:5\n"
	if m := tsLineRe.FindStringSubmatch(out); m == nil || m[1] != "100.84.75.31" {
		t.Fatalf("android peer not found: %v", m)
	}
}

func TestParseCloudflaredURL(t *testing.T) {
	quick := `2026-10-29T10:00:01Z INF |  https://quiet-otter-maple-rose.trycloudflare.com                                           |`
	if got := parseCloudflaredURL(quick); got != "https://quiet-otter-maple-rose.trycloudflare.com" {
		t.Fatalf("quick tunnel url: %q", got)
	}
	named := `2026-10-29T10:00:02Z INF Registered tunnel connection connIndex=0 connection=3f0b location=sin01 protocol=quic`
	if got := parseCloudflaredURL(named); got != "" {
		t.Fatalf("named tunnel line must give no quick url, got %q", got)
	}
	if parseCloudflaredURL("Requesting new quick Tunnel on trycloudflare.com...") != "" {
		t.Fatal("banner line is not a url")
	}
}

func TestBuildLinkJSON(t *testing.T) {
	lc := linkConfig{Token: "tok", Port: 8766}
	m := buildLinkJSON(lc, pcRoutes{LanIP: "192.168.1.2", TailscaleIP: ""}, "aa:bb", "pc")
	if _, ok := m["public_host"]; ok {
		t.Fatal("public_host must be absent when unset")
	}
	if _, ok := m["tunnel_url"]; ok {
		t.Fatal("tunnel_url must be absent when unset")
	}
	if m["token"] != "tok" || m["port"] != 8766 || m["lan_ip"] != "192.168.1.2" {
		t.Fatalf("base keys: %v", m)
	}
	m = buildLinkJSON(lc, pcRoutes{PublicHost: "home.duckdns.org", TunnelURL: "https://x.trycloudflare.com"}, "", "pc")
	if m["public_host"] != "home.duckdns.org" || m["tunnel_url"] != "https://x.trycloudflare.com" {
		t.Fatalf("routes missing: %v", m)
	}
}

func TestIsCGNAT(t *testing.T) {
	if !isCGNAT("100.70.1.2", "203.0.113.5") || !isCGNAT("192.168.0.2", "203.0.113.5") || !isCGNAT("203.0.113.9", "203.0.113.5") {
		t.Fatal("should be CGNAT")
	}
	if isCGNAT("203.0.113.5", "203.0.113.5") {
		t.Fatal("same public address is not CGNAT")
	}
}
