package main

import (
	"strings"
	"testing"
	"time"
)

// fakeAdb replaces adbRun: devices returns before() until a connect, then after().
func fakeAdb(t *testing.T, before, after string) *[]string {
	t.Helper()
	var calls []string
	connected := false
	old := adbRun
	adbRun = func(_ time.Duration, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[0] {
		case "devices":
			if connected {
				return after, nil
			}
			return before, nil
		case "connect":
			connected = true
			return "connected to " + args[1], nil
		}
		return "", nil
	}
	t.Cleanup(func() { adbRun = old })
	return &calls
}

func TestAdbTCPTarget(t *testing.T) {
	if got := adbTCPTarget(phoneConfig{AdbTCP: "100.84.75.31:5555", TSIP: "100.1.1.1"}); got != "100.84.75.31:5555" {
		t.Fatalf("AdbTcp first: %q", got)
	}
	if got := adbTCPTarget(phoneConfig{TSIP: "100.1.1.1"}); got != "100.1.1.1:5555" {
		t.Fatalf("tailscale ip fallback: %q", got)
	}
	if adbTCPTarget(phoneConfig{LanIP: "192.168.1.5"}) != "" {
		t.Fatal("no tailscale: no tcp target")
	}
}

func TestAdbStates(t *testing.T) {
	out := "List of devices attached\r\n100.84.75.31:5555      device product:e3qxxx model:SM_S928B\r\nadb-R5CX1-AbC._adb-tls-connect._tcp\toffline\r\n"
	st := adbStates(out)
	if st["100.84.75.31:5555"] != "device" || st["adb-R5CX1-AbC._adb-tls-connect._tcp"] != "offline" || len(st) != 2 {
		t.Fatalf("states: %v", st)
	}
}

func TestAdbRoutePrefersTailscaleOverStaleMDNS(t *testing.T) {
	before := "List of devices attached\nadb-R5CX1-AbC._adb-tls-connect._tcp\tdevice\n100.84.75.31:5555\toffline\n"
	after := "List of devices attached\nadb-R5CX1-AbC._adb-tls-connect._tcp\tdevice\n100.84.75.31:5555\tdevice\n"
	calls := fakeAdb(t, before, after)
	s, via, err := adbRoute(phoneConfig{AdbTCP: "100.84.75.31:5555"})
	if err != nil || s != "100.84.75.31:5555" || via != "tailscale tcp" {
		t.Fatalf("got %q %q %v", s, via, err)
	}
	j := strings.Join(*calls, "|")
	if !strings.Contains(j, "disconnect 100.84.75.31:5555|connect 100.84.75.31:5555") {
		t.Fatalf("offline entry must be disconnected before connecting: %s", j)
	}
}

func TestAdbRouteFallsBackToLAN(t *testing.T) {
	list := "List of devices attached\n192.168.1.50:41234\tdevice\n"
	fakeAdb(t, list, list)
	s, via, err := adbRoute(phoneConfig{AdbSerial: "192.168.1.50:41234"})
	if err != nil || s != "192.168.1.50:41234" || !strings.HasPrefix(via, "lan") {
		t.Fatalf("got %q %q %v", s, via, err)
	}
}

func TestAdbEnsureAlreadyConnected(t *testing.T) {
	calls := fakeAdb(t, "100.84.75.31:5555\tdevice\n", "")
	if !adbEnsure("100.84.75.31:5555") || len(*calls) != 1 {
		t.Fatalf("connected device needs only one devices call: %v", *calls)
	}
}
