//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The Linux admin helper is a root systemd service ("tokensaver-helper") that runs the
// commands TokenSaver marks elevated, without a sudo password each time. It listens on a
// Unix socket and only answers the user it was installed for, and only when the caller is
// that user's installed TokenSaver (checked through SO_PEERCRED and /proc/<pid>/exe).
// Destructive commands are refused unless the user confirmed them in chat. Every command
// is written to a root-owned log the user can read but not change.

const (
	helperSock     = "/run/tokensaver/helper.sock"
	helperConf     = "/etc/tokensaver/helper.json"
	helperBin      = "/usr/local/lib/tokensaver/tokensaver"
	helperUnit     = "/etc/systemd/system/tokensaver-helper.service"
	helperUnitName = "tokensaver-helper.service"
	helperLogDir   = "/var/log/tokensaver"
)

var errElevWindowsOnly = errors.New("the admin helper isn't installed (run: tokensaver install-helper)")

type helperConfig struct {
	UID    int    `json:"uid"`
	User   string `json:"user"`
	Home   string `json:"home"`
	Client string `json:"client"` // the user's installed TokenSaver
	Root   string `json:"root"`   // the user's TokenSaver folder (old copies after updates live here)
}

func loadHelperConfig() (helperConfig, error) {
	var c helperConfig
	b, err := os.ReadFile(helperConf)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func auditPath() string { return filepath.Join(helperLogDir, "elevated.log") }

var auditMu sync.Mutex

func audit(format string, a ...any) {
	auditMu.Lock()
	defer auditMu.Unlock()
	os.MkdirAll(helperLogDir, 0o755)
	f, err := os.OpenFile(auditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

// ---------- helper (root) ----------

func serveElevatedHelper() int {
	if os.Geteuid() != 0 {
		fmt.Println("the helper runs as root (systemd starts it); install it with: tokensaver install-helper")
		return 1
	}
	cfg, err := loadHelperConfig()
	if err != nil || cfg.UID <= 0 {
		audit("HELPER no valid %s: %v", helperConf, err)
		return 1
	}
	os.MkdirAll(filepath.Dir(helperSock), 0o755)
	os.Remove(helperSock)
	l, err := net.Listen("unix", helperSock)
	if err != nil {
		audit("HELPER couldn't listen on %s: %v", helperSock, err)
		return 1
	}
	// Anyone may connect; the peer check below decides who is served.
	os.Chmod(helperSock, 0o666)
	audit("HELPER started (pid %d, version %s) for %s (uid %d)", os.Getpid(), appVersion, cfg.User, cfg.UID)
	for {
		c, err := l.Accept()
		if err != nil {
			audit("HELPER accept failed: %v", err)
			time.Sleep(time.Second)
			continue
		}
		go handleElevated(c.(*net.UnixConn), cfg)
	}
}

func peerCred(c *net.UnixConn) (*syscall.Ucred, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var cred *syscall.Ucred
	var cerr error
	raw.Control(func(fd uintptr) {
		cred, cerr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	return cred, cerr
}

// helperClientOK: the installed program, or an older copy kept in the TokenSaver folder after an update.
func helperClientOK(client string, cfg helperConfig) bool {
	client = strings.TrimSuffix(client, " (deleted)")
	if samePath(client, cfg.Client) {
		return true
	}
	return strings.HasPrefix(filepath.Base(client), "tokensaver.old") && cfg.Root != "" && hasPathPrefix(client, cfg.Root)
}

func handleElevated(c *net.UnixConn, cfg helperConfig) {
	defer c.Close()
	defer func() {
		if r := recover(); r != nil {
			audit("ERROR internal: %v", r)
		}
	}()
	enc := json.NewEncoder(c)
	var sendMu sync.Mutex
	send := func(m elevMsg) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		c.SetWriteDeadline(time.Now().Add(30 * time.Second))
		return enc.Encode(m)
	}
	cred, err := peerCred(c)
	if err != nil {
		return
	}
	client, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", cred.Pid))
	if client == "" && int(cred.Uid) == cfg.UID {
		return // a status check that already hung up
	}
	if int(cred.Uid) != cfg.UID || !helperClientOK(client, cfg) {
		audit("REJECTED connection from pid %d uid %d (%s) — only %s of uid %d may use the helper", cred.Pid, cred.Uid, client, cfg.Client, cfg.UID)
		send(elevMsg{Error: "refused: only the installed TokenSaver of " + cfg.User + " may use the admin helper"})
		return
	}
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	line, err := br.ReadBytes('\n')
	if err != nil {
		return
	}
	c.SetReadDeadline(time.Time{})
	var req elevRequest
	if err := json.Unmarshal(line, &req); err != nil || strings.TrimSpace(req.Command) == "" {
		send(elevMsg{Error: "bad request"})
		return
	}
	if req.Command == "\x00upgrade" {
		helperUpgrade(cfg, send)
		return
	}
	shell := req.Shell
	if shell == "" || shell == "cmd" {
		shell = "bash"
	}
	if m := destructiveMatch(req.Command); m != "" && !req.Confirmed {
		audit("REFUSED (not confirmed, matched %q)  shell=%s  cwd=%s  cmd=%s", m, shell, req.Cwd, oneLine(req.Command))
		send(elevMsg{Error: fmt.Sprintf("refused: %q can delete or change system settings. Ask the user in chat, and only after they say yes run it again with confirmed: true.", m)})
		return
	}
	if req.Cwd != "" {
		if fi, err := os.Stat(req.Cwd); err != nil || !fi.IsDir() {
			send(elevMsg{Error: "folder not found: " + req.Cwd})
			return
		}
	}
	cmd, err := buildCommand(shell, req.Command)
	if err != nil {
		send(elevMsg{Error: err.Error()})
		return
	}
	cmd.Dir = req.Cwd
	cmd.Env = append(rootEnv(cfg), req.Env...)
	pr, pw, err := os.Pipe()
	if err != nil {
		send(elevMsg{Error: err.Error()})
		return
	}
	defer pr.Close()
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		audit("FAILED to start  shell=%s  cwd=%s  cmd=%s  — %v", shell, req.Cwd, oneLine(req.Command), err)
		send(elevMsg{Error: "couldn't start: " + err.Error()})
		return
	}
	pw.Close()
	pid := cmd.Process.Pid
	started := time.Now()
	confirmed := ""
	if req.Confirmed {
		confirmed = "  (confirmed by user)"
	}
	audit("START pid=%d  shell=%s  cwd=%s  cmd=%s%s", pid, shell, req.Cwd, oneLine(req.Command), confirmed)
	send(elevMsg{PID: pid})

	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		buf := make([]byte, 32<<10)
		for {
			n, err := pr.Read(buf)
			if n > 0 && send(elevMsg{Out: append([]byte(nil), buf[:n]...)}) != nil {
				io.Copy(io.Discard, pr)
				return
			}
			if err != nil {
				return
			}
		}
	}()
	gone := make(chan struct{}) // TokenSaver closed the connection: canceled, timed out, or it quit
	go func() { io.Copy(io.Discard, br); close(gone) }()
	waitDone := make(chan struct{})
	go func() { cmd.Wait(); close(waitDone) }()
	canceled := false
	select {
	case <-waitDone:
	case <-gone:
		canceled = true
		killTree(pid)
		<-waitDone
	}
	select {
	case <-outDone:
	case <-time.After(3 * time.Second): // something it started still holds the output open
	}
	code := cmd.ProcessState.ExitCode()
	note := ""
	if canceled {
		note = "  (stopped by TokenSaver)"
	}
	audit("END   pid=%d  exit=%d  after %s%s", pid, code, hd(time.Since(started)), note)
	send(elevMsg{Exit: &code})
}

// rootEnv: root's environment with the user's name known (SUDO_USER etc.), like sudo.
func rootEnv(cfg helperConfig) []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin",
		"HOME=/root", "USER=root", "LOGNAME=root", "LANG=C.UTF-8", "DEBIAN_FRONTEND=noninteractive",
		"SUDO_USER=" + cfg.User, "SUDO_UID=" + strconv.Itoa(cfg.UID), "TOKENSAVER_ELEVATED=1",
	}
}

// helperUpgrade copies the user's newly installed TokenSaver over the helper's own
// root-owned copy and exits; systemd restarts it on the new version.
func helperUpgrade(cfg helperConfig, send func(elevMsg) error) {
	if !exists(cfg.Client) {
		send(elevMsg{Error: "installed TokenSaver not found: " + cfg.Client})
		return
	}
	tmp := helperBin + ".new"
	if err := copyPlain(cfg.Client, tmp); err != nil {
		send(elevMsg{Error: "couldn't copy the new version: " + err.Error()})
		return
	}
	os.Chown(tmp, 0, 0)
	os.Chmod(tmp, 0o755)
	if err := os.Rename(tmp, helperBin); err != nil {
		send(elevMsg{Error: err.Error()})
		return
	}
	audit("UPGRADED helper from %s", cfg.Client)
	code := 0
	send(elevMsg{PID: os.Getpid()})
	send(elevMsg{Exit: &code})
	go func() { time.Sleep(300 * time.Millisecond); os.Exit(0) }()
}

// ---------- client ----------

func helperUp() bool {
	c, err := net.DialTimeout("unix", helperSock, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func startElevated(req elevRequest, out io.Writer) (*elevProc, error) {
	if os.Geteuid() == 0 { // already root: run it here, with the same confirmation rule
		return startElevatedLocal(req, out)
	}
	conn, err := net.DialTimeout("unix", helperSock, 3*time.Second)
	if err != nil {
		if exists(helperUnit) {
			return nil, errors.New("the admin helper is installed but not running (sudo systemctl start tokensaver-helper)")
		}
		return nil, errElevWindowsOnly
	}
	uc := conn.(*net.UnixConn)
	if cred, err := peerCred(uc); err != nil || cred.Uid != 0 {
		conn.Close()
		return nil, errors.New("refused: the admin helper socket isn't owned by root")
	}
	b, _ := json.Marshal(req)
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		conn.SetReadDeadline(time.Now().Add(time.Second))
		var m elevMsg
		if line, _ := bufio.NewReader(conn).ReadBytes('\n'); json.Unmarshal(line, &m) == nil && m.Error != "" {
			conn.Close()
			return nil, errors.New(m.Error)
		}
		conn.Close()
		return nil, fmt.Errorf("couldn't talk to the admin helper: %v", err)
	}
	conn.SetWriteDeadline(time.Time{})
	br := bufio.NewReaderSize(conn, 64<<10)
	conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	line, err := br.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("no answer from the admin helper: %v", err)
	}
	conn.SetReadDeadline(time.Time{})
	var m elevMsg
	json.Unmarshal(line, &m)
	if m.Error != "" || m.PID == 0 {
		conn.Close()
		if m.Error == "" {
			m.Error = "bad answer from the admin helper"
		}
		return nil, errors.New(m.Error)
	}
	var once sync.Once
	stopped := false
	var stopMu sync.Mutex
	ep := &elevProc{pid: m.PID, done: make(chan struct{})}
	ep.stop = func() {
		once.Do(func() {
			stopMu.Lock()
			stopped = true
			stopMu.Unlock()
			conn.Close()
		})
	}
	go func() {
		defer close(ep.done)
		defer conn.Close()
		for {
			line, err := br.ReadBytes('\n')
			if err != nil {
				stopMu.Lock()
				s := stopped
				stopMu.Unlock()
				if s {
					ep.err = errCanceled
				} else {
					ep.err = fmt.Errorf("lost the connection to the admin helper: %v", err)
				}
				return
			}
			var m elevMsg
			if json.Unmarshal(line, &m) != nil {
				continue
			}
			if len(m.Out) > 0 {
				out.Write(m.Out)
			}
			if m.Error != "" {
				ep.err = errors.New(m.Error)
				return
			}
			if m.Exit != nil {
				ep.code = m.Exit
				return
			}
		}
	}()
	return ep, nil
}

// startElevatedLocal: TokenSaver itself runs as root (e.g. under sudo).
func startElevatedLocal(req elevRequest, out io.Writer) (*elevProc, error) {
	if m := destructiveMatch(req.Command); m != "" && !req.Confirmed {
		return nil, fmt.Errorf("refused: %q can delete or change system settings. Ask the user in chat, and only after they say yes run it again with confirmed: true.", m)
	}
	cmd, err := buildCommand(req.Shell, req.Command)
	if err != nil {
		return nil, err
	}
	cmd.Dir = req.Cwd
	cmd.Env = append(os.Environ(), req.Env...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ep := &elevProc{pid: cmd.Process.Pid, done: make(chan struct{})}
	ep.stop = func() { killTree(cmd.Process.Pid) }
	go func() {
		defer close(ep.done)
		cmd.Wait()
		code := cmd.ProcessState.ExitCode()
		ep.code = &code
	}()
	return ep, nil
}

// ---------- install / uninstall ----------

// asRoot re-runs this program as root: pkexec on a desktop (password dialog), sudo in a terminal.
func asRoot(args ...string) error {
	exe, _ := os.Executable()
	h, _ := os.UserHomeDir()
	env := []string{"TOKENSAVER_FOR_UID=" + strconv.Itoa(os.Getuid()), "TOKENSAVER_FOR_HOME=" + h, "TOKENSAVER_ROOT=" + appRoot()}
	var c *exec.Cmd
	if _, err := exec.LookPath("pkexec"); err == nil && os.Getenv("DISPLAY")+os.Getenv("WAYLAND_DISPLAY") != "" {
		c = exec.Command("pkexec", append(append([]string{"env"}, env...), append([]string{exe}, args...)...)...)
	} else {
		c = exec.Command("sudo", append(append([]string{"env"}, env...), append([]string{exe}, args...)...)...)
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func installingUser() (helperConfig, error) {
	uidS := os.Getenv("TOKENSAVER_FOR_UID")
	if uidS == "" {
		uidS = os.Getenv("SUDO_UID")
	}
	if uidS == "" {
		uidS = os.Getenv("PKEXEC_UID")
	}
	uid, _ := strconv.Atoi(uidS)
	if uid <= 0 {
		return helperConfig{}, errors.New("run install-helper as your normal user (it asks for your password once)")
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return helperConfig{}, err
	}
	root := os.Getenv("TOKENSAVER_ROOT")
	if root == "" {
		root = filepath.Join(u.HomeDir, "Claude Apps", "TokenSaver")
	}
	return helperConfig{UID: uid, User: u.Username, Home: u.HomeDir, Root: root, Client: filepath.Join(root, "app", "tokensaver")}, nil
}

const helperUnitText = `[Unit]
Description=TokenSaver admin helper (runs the admin commands TokenSaver hands it; log: ` + helperLogDir + `/elevated.log)
After=local-fs.target

[Service]
Type=simple
ExecStart=` + helperBin + ` helper
Restart=always
RestartSec=2
UMask=0022

[Install]
WantedBy=multi-user.target
`

func consoleInstallHelper() {
	if os.Geteuid() != 0 {
		fmt.Println("Installing the admin helper needs your password once…")
		if err := asRoot("install-helper"); err != nil {
			fmt.Println("Helper not installed:", err)
		}
		return
	}
	cfg, err := installingUser()
	if err != nil {
		fmt.Println(err)
		return
	}
	if !exists(cfg.Client) {
		fmt.Println("Install TokenSaver first (tokensaver install), then the helper.")
		return
	}
	for _, d := range []string{filepath.Dir(helperBin), filepath.Dir(helperConf), helperLogDir} {
		os.MkdirAll(d, 0o755)
	}
	tmp := helperBin + ".new"
	if err := copyPlain(cfg.Client, tmp); err != nil {
		fmt.Println("Couldn't copy TokenSaver:", err)
		return
	}
	os.Chmod(tmp, 0o755)
	if err := os.Rename(tmp, helperBin); err != nil {
		fmt.Println(err)
		return
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(helperConf, b, 0o644); err != nil {
		fmt.Println(err)
		return
	}
	if err := os.WriteFile(helperUnit, []byte(helperUnitText), 0o644); err != nil {
		fmt.Println(err)
		return
	}
	for _, a := range [][]string{{"daemon-reload"}, {"enable", helperUnitName}, {"restart", helperUnitName}} {
		if out, err := exec.Command("systemctl", a...).CombinedOutput(); err != nil {
			fmt.Printf("systemctl %s: %s\n", strings.Join(a, " "), strings.TrimSpace(string(out)))
			return
		}
	}
	audit("INSTALLED helper for %s (uid %d), client %s", cfg.User, cfg.UID, cfg.Client)
	fmt.Println("Admin helper installed and running for", cfg.User)
	fmt.Println("Every elevated command is logged in", auditPath())
}

func consoleUninstallHelper() {
	if os.Geteuid() != 0 {
		fmt.Println("Removing the admin helper needs your password once…")
		if err := asRoot("uninstall-helper"); err != nil {
			fmt.Println("Helper not removed:", err)
		}
		return
	}
	exec.Command("systemctl", "disable", "--now", helperUnitName).Run()
	os.Remove(helperUnit)
	exec.Command("systemctl", "daemon-reload").Run()
	os.Remove(helperBin)
	os.Remove(helperConf)
	os.Remove(helperSock)
	audit("UNINSTALLED helper")
	fmt.Println("Admin helper removed. The log is kept at", auditPath())
}

// refreshHelper moves an installed helper onto the newly installed program (no password:
// the helper replaces its own copy and systemd restarts it).
func refreshHelper() {
	if !exists(helperUnit) || os.Geteuid() == 0 {
		return
	}
	// The helper only answers the installed program, so the installer asks that copy to do it.
	if self, _ := os.Executable(); !samePath(self, installPath()) && exists(installPath()) {
		c := exec.Command(installPath(), "refresh-helper")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		c.Run()
		return
	}
	var out strings.Builder
	ep, err := startElevated(elevRequest{Command: "\x00upgrade"}, &out)
	if err != nil {
		fmt.Println("Admin helper not updated:", err, "(run tokensaver install-helper)")
		return
	}
	<-ep.done
	if ep.err != nil {
		fmt.Println("Admin helper not updated:", ep.err)
		return
	}
	for i := 0; i < 25 && !helperUp(); i++ {
		time.Sleep(200 * time.Millisecond)
	}
	fmt.Println("Admin helper restarted on this version.")
}

func helperStatus() string {
	switch {
	case helperUp():
		return "running (log: " + auditPath() + ")"
	case exists(helperUnit):
		return "installed, not running"
	}
	return "not installed"
}

// moveTree merges src into dst and removes src once everything is moved.
func moveTree(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if !exists(dst) {
		if err := os.Rename(src, dst); err == nil || !isCrossDevice(err) {
			return err
		}
	}
	var dirs []string
	err := filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if fi.IsDir() {
			dirs = append(dirs, p)
			return os.MkdirAll(t, fi.Mode().Perm()|0o700)
		}
		if exists(t) {
			return nil
		}
		if err := os.Rename(p, t); err != nil {
			if !isCrossDevice(err) {
				return err
			}
			if err := copyPlain(p, t); err != nil {
				return err
			}
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		os.Remove(dirs[i]) // only empty folders go; files that already existed in dst stay in src
	}
	if exists(src) {
		return fmt.Errorf("some files already existed in %s and stayed in %s", dst, src)
	}
	return nil
}
