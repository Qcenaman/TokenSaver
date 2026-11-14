package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOtherDevice(t *testing.T) {
	pc := phoneConfig{LanIP: "192.168.1.132", TSIP: "100.84.75.31"}
	cases := []struct {
		lan, ts, from string
		want          bool
	}{
		{"192.168.1.132", "100.84.75.31", "100.84.75.31", false},
		{"192.168.1.140", "100.84.75.31", "192.168.1.140", false}, // the phone got a new LAN address
		{"", "", "127.0.0.1", false},
		{"192.168.1.108", "100.108.148.21", "100.108.148.21", true}, // the TV
		{"192.168.1.108", "", "192.168.1.108", true},
		{"", "", "100.108.148.21", true},                       // over Tailscale from the TV, whatever it reports
		{"192.168.1.132", "100.84.75.31", "127.0.0.1", false},  // the phone through the tunnel / Funnel
		{"10.20.30.40", "100.84.75.31", "100.84.75.31", false}, // the phone on mobile data, over Tailscale
	}
	for _, c := range cases {
		if got := otherDevice(pc, c.lan, c.ts, c.from); got != c.want {
			t.Errorf("otherDevice(%q,%q from %q)=%v want %v", c.lan, c.ts, c.from, got, c.want)
		}
	}
	if otherDevice(phoneConfig{}, "1.2.3.4", "100.1.1.1", "100.1.1.1") {
		t.Error("unpaired PC: the reporter is the phone")
	}
}

// the TV reporting in must not overwrite the phone's addresses, and still gets the PC's routes
func TestReportFromOtherDeviceKeepsPhone(t *testing.T) {
	appHome = t.TempDir()
	t.Cleanup(func() { phoneMu.Lock(); phoneCfg, phoneLoaded = phoneConfig{}, false; phoneMu.Unlock() })
	savePhoneCfg(phoneConfig{LanIP: "192.168.1.132", TSIP: "100.84.75.31", Token: "tok"})
	h := linkHandler(linkConfig{Token: "link"})
	report := func(body, from string) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(t.Context(), "POST", "/phone/report", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer link")
		r.RemoteAddr = from + ":40000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := report(`{"LanIP":"192.168.1.108","TailscaleIP":"100.108.148.21","Network":"wifi"}`, "100.108.148.21"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "routes") {
		t.Fatalf("the TV's report: %d %s", w.Code, w.Body.String())
	}
	if pc := loadPhoneCfg(); pc.LanIP != "192.168.1.132" || pc.TSIP != "100.84.75.31" || pc.Token != "tok" {
		t.Fatalf("the TV changed the phone's settings: %+v", pc)
	}
	if d := loadOtherDevices()["100.108.148.21"]; d.LanIP != "192.168.1.108" {
		t.Fatalf("the TV was not kept as another device: %+v", d)
	}
	report(`{"LanIP":"192.168.1.150","TailscaleIP":"100.84.75.31","Network":"wifi"}`, "100.84.75.31")
	if pc := loadPhoneCfg(); pc.LanIP != "192.168.1.150" {
		t.Errorf("the phone's own report must still update it: %+v", pc)
	}
	if !strings.Contains(otherDevicesStatus(), "100.108.148.21") {
		t.Error("phone status should list the TV as another device")
	}
}
