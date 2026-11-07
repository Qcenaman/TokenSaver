//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// "tokensaver serve" starts at boot as a systemd user service. With lingering on, it runs
// from boot even before anyone logs in (the Windows version is a boot-time service too).

const serveUnitName = "tokensaver-serve.service"

func runServeService() bool { return false }

func userUnitDir() string { return filepath.Join(configHome(), "systemd", "user") }

// serveSingleInstance holds an flock on data/serve.lock while serve runs.
func serveSingleInstance() (func(), error) {
	p := filepath.Join(appHome, "serve.lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("serve.lock is held by another process")
	}
	f.Truncate(0)
	fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func userctl(args ...string) (string, error) {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// consoleInstallLink installs and starts the serve service, opens the firewall port and
// asks once whether it may start at boot before login.
func consoleInstallLink() {
	exe := installPath()
	if !exists(exe) {
		exe, _ = os.Executable()
	}
	lc := loadLinkCfg()
	unit := fmt.Sprintf(`[Unit]
Description=TokenSaver serve (/mcp and /health on port %d)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s serve
Restart=always
RestartSec=3
KillSignal=SIGINT
TimeoutStopSec=15

[Install]
WantedBy=default.target
`, lc.Port, systemdQuote(exe))
	os.MkdirAll(userUnitDir(), 0o755)
	if err := os.WriteFile(filepath.Join(userUnitDir(), serveUnitName), []byte(unit), 0o644); err != nil {
		fmt.Println("Couldn't write the service:", err)
		return
	}
	for _, a := range [][]string{{"daemon-reload"}, {"enable", serveUnitName}, {"restart", serveUnitName}} {
		if out, err := userctl(a...); err != nil {
			fmt.Printf("systemctl --user %s: %s\n", strings.Join(a, " "), out)
			fmt.Println("Start it by hand with:", exe, "serve")
			return
		}
	}
	fmt.Println("TokenSaver serve is installed as a user service and running (", serveUnitName, ")")
	if u, err := user.Current(); err == nil && !lingerOn(u.Username) {
		if askYes("Start it at boot, before you log in? This turns on lingering for your account (needs your password once). [Y/n] ", true) {
			if err := enableLinger(u.Username); err != nil {
				fmt.Println("Lingering not turned on:", err)
			} else {
				fmt.Println("It now starts at boot.")
			}
		}
	}
	fmt.Println(firewallAllowLink(lc.Port))
	time.Sleep(time.Second)
	fmt.Println(linkStatusReport())
}

func lingerOn(name string) bool { return exists(filepath.Join("/var/lib/systemd/linger", name)) }

func enableLinger(name string) error {
	if helperUp() {
		var out strings.Builder
		ep, err := startElevated(elevRequest{Command: "loginctl enable-linger " + shQuote(name)}, &out)
		if err == nil {
			<-ep.done
			if ep.err == nil && ep.code != nil && *ep.code == 0 {
				return nil
			}
		}
	}
	c := exec.Command("loginctl", "enable-linger", name) // polkit asks for the password when needed
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func linkStatusReport() string {
	lc := loadLinkCfg()
	svc := "service: not installed (tokensaver install-link)"
	if exists(filepath.Join(userUnitDir(), serveUnitName)) {
		st, _ := userctl("is-active", serveUnitName)
		svc = "service: " + st
	}
	if phoneHealth(fmt.Sprintf("http://127.0.0.1:%d", lc.Port), time.Second) {
		return fmt.Sprintf("port %d: listening · %s", lc.Port, svc)
	}
	return fmt.Sprintf("port %d: NOT listening (see %s) · %s", lc.Port, serveLogPath(), svc)
}

// ufwActive: the Ubuntu firewall is on (it's off by default on desktops).
func ufwActive() (bool, bool) {
	if _, err := exec.LookPath("ufw"); err != nil {
		return false, false
	}
	b, err := os.ReadFile("/etc/ufw/ufw.conf")
	if err != nil {
		return false, true
	}
	return strings.Contains(string(b), "ENABLED=yes"), true
}

// firewallAllowLinux opens the link port in ufw when ufw is on (through the admin helper).
func firewallAllowLinux(port int) string {
	on, has := ufwActive()
	if !has || !on {
		return "firewall: ufw is off, nothing to open"
	}
	cmd := fmt.Sprintf("ufw allow %d/tcp comment 'TokenSaver Link'", port)
	var out strings.Builder
	ep, err := startElevated(elevRequest{Command: cmd}, &out)
	if err != nil {
		return "firewall: admin helper unavailable (" + err.Error() + "); run: sudo " + cmd
	}
	<-ep.done
	if ep.err != nil || ep.code == nil || *ep.code != 0 {
		return "firewall: rule not added: " + strings.TrimSpace(out.String())
	}
	return fmt.Sprintf("firewall: TCP %d allowed inbound (ufw)", port)
}
