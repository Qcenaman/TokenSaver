package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestTailscaleAddressOfThePairedPhone(t *testing.T) {
	status := "100.101.102.103  ultimateviruscollection  qcenaman@  windows  -\n" +
		"100.108.148.21   galaxy-tab-s9-ultra      qcenaman@  android  offline\n" +
		"100.84.75.31     iris-s24-ultra           qcenaman@  android  active; direct 192.168.1.132:41641\n"
	if ips := tailscaleAndroidIPs(status); len(ips) != 2 || ips[0] != "100.108.148.21" || ips[1] != "100.84.75.31" {
		t.Errorf("android devices = %v", ips)
	}
	addr := "1: lo    inet 127.0.0.1/8 scope host lo\n" +
		"14: rmnet_data2    inet 100.70.12.9/30 scope global rmnet_data2\n" +
		"31: wlan0    inet 192.168.1.132/24 brd 192.168.1.255 scope global wlan0\n" +
		"42: tun0    inet 100.84.75.31/32 scope global tun0\n"
	if got := tunIPIn(addr); got != "100.84.75.31" {
		t.Errorf("tunIPIn = %q, want the tunnel's address", got)
	}
	if got := tunIPIn("14: rmnet_data2    inet 100.70.12.9/30 scope global rmnet_data2\n"); got != "" {
		t.Errorf("a mobile-data address is not the Tailscale one, got %q", got)
	}
	for ip, want := range map[string]bool{"100.84.75.31": true, "100.127.255.1": true, "100.128.0.1": false, "192.168.1.132": false, "": false} {
		if isTailnetIP(ip) != want {
			t.Errorf("isTailnetIP(%q) != %v", ip, want)
		}
	}
}

func TestKeepAnsweringIP(t *testing.T) {
	up := func(ips ...string) func(string) bool {
		return func(ip string) bool { return strings.Contains(" "+strings.Join(ips, " ")+" ", " "+ip+" ") }
	}
	for _, c := range []struct {
		old, found string
		answers    func(string) bool
		want       string
	}{
		{"100.84.75.31", "100.108.148.21", up("100.84.75.31"), "100.84.75.31"}, // the right one is not overwritten by a dead one
		{"100.108.148.21", "100.84.75.31", up("100.84.75.31"), "100.84.75.31"},
		{"100.108.148.21", "100.84.75.31", up(), "100.84.75.31"},
		{"", "100.84.75.31", up(), "100.84.75.31"},
		{"100.84.75.31", "", up(), "100.84.75.31"},
	} {
		if got := keepAnsweringIP(c.old, c.found, c.answers); got != c.want {
			t.Errorf("keepAnsweringIP(%q, %q) = %q, want %q", c.old, c.found, got, c.want)
		}
	}
}

func TestTokenFromBroadcast(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef"
	out := "Broadcasting: Intent { flg=0x400000 cmp=com.qcenaman.mobiletokensaver/.service.TokenReceiver }\nBroadcast completed: result=1, data=\"" + tok + "\"\n"
	if got := tokenFromBroadcast(out); got != tok {
		t.Errorf("token = %q", got)
	}
	for _, bad := range []string{"Broadcast completed: result=0", "Broadcast completed: result=0, data=\"Link to my PC is off\"", ""} {
		if got := tokenFromBroadcast(bad); got != "" {
			t.Errorf("no token expected in %q, got %q", bad, got)
		}
	}
}

// The phone answers 401 to a wrong token: pair must not come away with it.
func TestRejectedTokenIsNotPaired(t *testing.T) {
	const good = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+good {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	}))
	defer srv.Close()
	check := func(tok string) (bool, bool) { return phoneTokenCheck(srv.URL, tok) }
	if ok, answered := check("stale-token-0000000000"); ok || !answered {
		t.Errorf("a wrong token: accepted=%v answered=%v", ok, answered)
	}
	if tok, verdict := pickPhoneToken([]string{"stale-token-0000000000"}, check); tok != "" || verdict != "rejected" {
		t.Errorf("only a wrong token: got %q, %s", tok, verdict)
	}
	if tok, verdict := pickPhoneToken([]string{"stale-token-0000000000", good}, check); tok != good || verdict != "ok" {
		t.Errorf("the right token should be recovered: got %q, %s", tok, verdict)
	}
	dead := func(string) (bool, bool) { return false, false }
	if tok, verdict := pickPhoneToken([]string{good}, dead); tok != good || verdict != "unchecked" {
		t.Errorf("engine not answering: got %q, %s", tok, verdict)
	}
}

// push_routes: the route that answers is used, and what the phone said outranks a dead address.
func TestPushRoutesUsesTheRouteThatAnswers(t *testing.T) {
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/link" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	deadBase := "http://" + l.Addr().String()
	l.Close() // nothing listens here any more
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	p, _ := strconv.Atoi(port)
	c := phoneConfig{Token: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Port: p}
	if err := pushRoutesToPhone(c, []string{deadBase, srv.URL}); err != nil {
		t.Errorf("the answering route should be used: %v", err)
	}
	status = 401
	err = pushRoutesToPhone(c, []string{srv.URL, deadBase})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "phone pair") {
		t.Errorf("want the phone's 401 in the error, got %v", err)
	}
}
