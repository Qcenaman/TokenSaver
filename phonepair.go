package main

// phone pair's helpers: getting the app's token over adb, checking it against the engine before
// it is saved, and finding the Tailscale address of the phone being paired (not of whichever
// Android device the tailnet lists first).

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const phoneTokenHelp = `update MobileTokenSaver on the phone to 0.2.1 or newer (it hands its token to phone pair by itself), or open the app, scroll down to "Engine" (below "5 · Permissions"), copy Token and run phone pair again with token set`

var (
	phoneTokenRe = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
	amDataRe     = regexp.MustCompile(`data="([0-9a-fA-F]{16,})"`)
)

// tokenFromBroadcast reads the token out of `am broadcast`'s "Broadcast completed: result=1, data="..."" line.
func tokenFromBroadcast(out string) string {
	if m := amDataRe.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

// phoneAppToken asks the app for its token over adb: run-as works on debug builds, the app's
// TokenReceiver (MobileTokenSaver 0.2.1+) on release builds, where run-as is refused.
func phoneAppToken(serial, pkg string) string {
	if out, err := adbShell(serial, 15*time.Second, "run-as "+pkg+" cat files/token"); err == nil {
		if t := strings.TrimSpace(out); phoneTokenRe.MatchString(t) {
			return t
		}
	}
	out, _ := adbShell(serial, 15*time.Second, "am broadcast -n "+pkg+"/.service.TokenReceiver")
	return tokenFromBroadcast(out)
}

// phoneTokenCheck asks the engine at base whether it accepts token. answered is false when the
// engine gave no reply at all.
func phoneTokenCheck(base, token string) (accepted, answered bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/mcp", bytes.NewReader([]byte(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := phoneHTTP.Do(req)
	if err != nil {
		return false, false
	}
	resp.Body.Close()
	return resp.StatusCode != 401 && resp.StatusCode != 403, true
}

// pickPhoneToken returns the first candidate the engine accepts ("ok"). When the engine answers
// none of the checks the first candidate comes back "unchecked"; when it rejects them all,
// nothing comes back and the verdict is "rejected".
func pickPhoneToken(cands []string, check func(string) (accepted, answered bool)) (token, verdict string) {
	anyAnswer := false
	for _, t := range cands {
		ok, answered := check(t)
		if ok {
			return t, "ok"
		}
		anyAnswer = anyAnswer || answered
	}
	if !anyAnswer && len(cands) > 0 {
		return cands[0], "unchecked"
	}
	return "", "rejected"
}

// isTailnetIP: an address in Tailscale's range, 100.64.0.0/10.
func isTailnetIP(s string) bool {
	ip := net.ParseIP(strings.TrimSpace(s)).To4()
	return ip != nil && ip[0] == 100 && ip[1]&0xC0 == 64
}

var ipAddrLineRe = regexp.MustCompile(`(?m)^\d+:\s+(\S+)\s+inet\s+(\d+\.\d+\.\d+\.\d+)`)

// tunIPIn finds the Tailscale address in the phone's `ip -4 -o addr show` output: a 100.64/10
// address on a tunnel interface (mobile-data interfaces can sit in the same range, so they don't count).
func tunIPIn(out string) string {
	for _, m := range ipAddrLineRe.FindAllStringSubmatch(out, -1) {
		if (strings.HasPrefix(m[1], "tun") || strings.HasPrefix(m[1], "tailscale")) && isTailnetIP(m[2]) {
			return m[2]
		}
	}
	return ""
}

// tailscaleAndroidIPs lists every Android device in `tailscale status`.
func tailscaleAndroidIPs(status string) []string {
	var ips []string
	for _, m := range tsLineRe.FindAllStringSubmatch(status, -1) {
		ips = append(ips, m[1])
	}
	return ips
}

// phoneTailscaleIP is the Tailscale address of the phone behind serial: asked from the phone
// itself, else the adb address when adb already goes through Tailscale, else `tailscale status`
// when it lists exactly one Android device.
func phoneTailscaleIP(serial string) string {
	if out, _ := adbShell(serial, 10*time.Second, "ip -4 -o addr show"); out != "" {
		if ip := tunIPIn(out); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(serial); err == nil && isTailnetIP(host) {
		return host
	}
	if exe := tailscaleExe(); exe != "" {
		out, _ := runQuiet(8*time.Second, exe, "status")
		if ips := tailscaleAndroidIPs(out); len(ips) == 1 {
			return ips[0]
		}
	}
	return ""
}

// keepAnsweringIP: a stored address that answers is not replaced by a found one that doesn't.
func keepAnsweringIP(old, found string, answers func(ip string) bool) string {
	if found == "" {
		return old
	}
	if old == "" || old == found {
		return found
	}
	if answers(old) && !answers(found) {
		return old
	}
	return found
}
