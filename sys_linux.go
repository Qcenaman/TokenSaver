//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

func hideWindow(c *exec.Cmd) {}

// shBin is the POSIX shell; Android keeps it under /system/bin.
func shBin() string {
	if runtime.GOOS == "android" {
		return "/system/bin/sh"
	}
	return "/bin/sh"
}

// bashBin is the shell "run" uses by default; plain sh when bash is missing.
func bashBin() string {
	for _, p := range []string{"/bin/bash", "/usr/bin/bash"} {
		if exists(p) {
			return p
		}
	}
	return shBin()
}

// buildCommand keeps the Windows shell names so Claude can call both versions the
// same way: "" and "cmd" mean bash here, "powershell"/"pwsh" mean PowerShell 7 if installed.
func buildCommand(shell, command string) (*exec.Cmd, error) {
	var c *exec.Cmd
	switch strings.ToLower(shell) {
	case "", "cmd", "bash":
		c = exec.Command(bashBin(), "-c", command)
	case "sh":
		c = exec.Command(shBin(), "-c", command)
	case "powershell", "pwsh":
		ps, err := exec.LookPath("pwsh")
		if err != nil {
			return nil, errors.New("PowerShell isn't installed (sudo snap install powershell --classic); use the default bash shell")
		}
		c = exec.Command(ps, "-NoProfile", "-NonInteractive", "-Command", command)
	default:
		return nil, fmt.Errorf("unknown shell %q (use bash, sh or pwsh)", shell)
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c, nil
}

func killTree(pid int) {
	if syscall.Kill(-pid, syscall.SIGKILL) != nil {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	for _, c := range childPids(pid) {
		killTree(c)
	}
}

// childPids reads /proc/<pid>/task/*/children.
func childPids(pid int) []int {
	var out []int
	tasks, _ := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", pid))
	for _, t := range tasks {
		b, _ := os.ReadFile(t)
		for _, f := range strings.Fields(string(b)) {
			if n, err := strconv.Atoi(f); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func pidAlive(pid int) bool {
	if syscall.Kill(pid, 0) != nil && !errors.Is(syscall.Kill(pid, 0), syscall.EPERM) {
		return false
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err == nil {
		if i := strings.LastIndexByte(string(b), ')'); i > 0 {
			f := strings.Fields(string(b)[i+1:])
			if len(f) > 0 && f[0] == "Z" {
				return false
			}
		}
	}
	return true
}

// claudeRunning: the Claude desktop app (any Linux build) is open.
func claudeRunning() bool {
	pid, _, _ := claudeMain()
	return pid > 0
}

func osRelease() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"`)
		}
	}
	return runtime.GOOS
}

func platformInfo() []string {
	var uts syscall.Utsname
	kernel := ""
	if syscall.Uname(&uts) == nil {
		kernel = " · kernel " + utsString(uts.Release[:])
	}
	host, _ := os.Hostname()
	session := os.Getenv("XDG_SESSION_TYPE")
	if session == "" {
		session = "no desktop session"
	}
	lines := []string{fmt.Sprintf("OS: %s (%s)%s · %s · host %s", osRelease(), runtime.GOARCH, kernel, session, host)}
	cpu := fmt.Sprintf("%d logical cores", runtime.NumCPU())
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "model name") {
				if _, v, ok := strings.Cut(l, ":"); ok {
					cpu = strings.TrimSpace(v) + ", " + cpu
				}
				break
			}
		}
	}
	lines = append(lines, "CPU: "+cpu)
	if total, avail := memInfo(); total > 0 {
		lines = append(lines, fmt.Sprintf("RAM: %s free of %s", hb(avail), hb(total)))
	}
	var disks []string
	for _, m := range realMounts() {
		var st syscall.Statfs_t
		if syscall.Statfs(m.dir, &st) == nil && st.Blocks > 0 {
			disks = append(disks, fmtDisk(m.dir, m.fs, st.Bavail*uint64(st.Bsize), st.Blocks*uint64(st.Bsize)))
		}
	}
	if len(disks) > 0 {
		lines = append(lines, "Disks: "+strings.Join(disks, "; "))
	}
	return lines
}

func utsString(a []int8) string {
	b := make([]byte, 0, len(a))
	for _, c := range a {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

func memInfo() (total, avail int64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) >= 2 {
			v, _ := strconv.ParseInt(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				total = v * 1024
			case "MemAvailable:":
				avail = v * 1024
			}
		}
	}
	return
}

type mountInfo struct{ dev, dir, fs string }

// realMounts: mounted disks (ext4, btrfs, ntfs, vfat, exfat, xfs...), not snaps or pseudo filesystems.
func realMounts() []mountInfo {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return []mountInfo{{"", "/", ""}}
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []mountInfo
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		x := strings.Fields(sc.Text())
		if len(x) < 3 || !strings.HasPrefix(x[0], "/dev/") || strings.HasPrefix(x[0], "/dev/loop") || x[2] == "squashfs" {
			continue
		}
		dir := strings.ReplaceAll(x[1], `\040`, " ")
		if seen[x[0]] || strings.HasPrefix(dir, "/snap/") || strings.HasPrefix(dir, "/boot/efi") && x[2] == "vfat" && dir != "/boot/efi" {
			continue
		}
		seen[x[0]] = true
		out = append(out, mountInfo{x[0], dir, x[2]})
	}
	if len(out) == 0 {
		out = []mountInfo{{"", "/", ""}}
	}
	return out
}

// procs reads /proc directly: full names (ps truncates them to 15 characters) and command lines.
func procs() ([]procInfo, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	page := int64(os.Getpagesize())
	var ps []procInfo
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdB, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		args := strings.Split(strings.TrimRight(string(cmdB), "\x00"), "\x00")
		name := ""
		if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
			name = filepath.Base(strings.TrimSuffix(exe, " (deleted)"))
		} else if len(args) > 0 && args[0] != "" {
			name = filepath.Base(args[0])
		} else if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
			name = strings.TrimSpace(string(b))
		} else {
			continue
		}
		var rss int64
		if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid)); err == nil {
			if f := strings.Fields(string(b)); len(f) > 1 {
				n, _ := strconv.ParseInt(f[1], 10, 64)
				rss = n * page
			}
		}
		ps = append(ps, procInfo{pid: pid, name: name, mem: rss, cmd: strings.Join(args, " ")})
	}
	return ps, nil
}

func procNames() map[int]string {
	m := map[int]string{}
	ps, _ := procs()
	for _, p := range ps {
		m[p.pid] = p.name
	}
	return m
}

// socketOwners maps socket inodes to the pid holding them (only this user's processes unless root).
func socketOwners() map[uint64]int {
	m := map[uint64]int{}
	fds, _ := filepath.Glob("/proc/[0-9]*/fd/*")
	for _, fd := range fds {
		l, err := os.Readlink(fd)
		if err != nil || !strings.HasPrefix(l, "socket:[") {
			continue
		}
		ino, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(l, "socket:["), "]"), 10, 64)
		if err != nil {
			continue
		}
		parts := strings.Split(fd, "/")
		if pid, err := strconv.Atoi(parts[2]); err == nil {
			if _, ok := m[ino]; !ok {
				m[ino] = pid
			}
		}
	}
	return m
}

type rawListen struct {
	proto, addr string
	port        int
	inode       uint64
}

// procNetListen reads TCP sockets in LISTEN state and bound UDP sockets.
func procNetListen() []rawListen {
	var out []rawListen
	for _, src := range []struct{ file, proto string }{{"tcp", "TCP"}, {"tcp6", "TCP"}, {"udp", "UDP"}, {"udp6", "UDP"}} {
		b, err := os.ReadFile("/proc/net/" + src.file)
		if err != nil {
			continue
		}
		for i, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if i == 0 || len(f) < 10 {
				continue
			}
			if src.proto == "TCP" && f[3] != "0A" { // 0A = LISTEN
				continue
			}
			if src.proto == "UDP" && f[3] != "07" { // 07 = unconnected (bound)
				continue
			}
			addr, port := parseHexAddr(f[1])
			ino, _ := strconv.ParseUint(f[9], 10, 64)
			out = append(out, rawListen{src.proto, addr, port, ino})
		}
	}
	return out
}

func parseHexAddr(s string) (string, int) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0
	}
	port, _ := strconv.ParseInt(p, 16, 32)
	switch len(h) {
	case 8:
		v, _ := strconv.ParseUint(h, 16, 32)
		return fmt.Sprintf("%d.%d.%d.%d", v&0xff, v>>8&0xff, v>>16&0xff, v>>24), int(port)
	case 32:
		if h == strings.Repeat("0", 32) {
			return "::", int(port)
		}
		if h == "0000000000000000FFFF000000000000" {
			return "::ffff:0.0.0.0", int(port)
		}
		if h == "00000000000000000000000001000000" {
			return "::1", int(port)
		}
		return "[" + h + "]", int(port)
	}
	return h, int(port)
}

func listening() ([]portInfo, error) {
	raw := procNetListen()
	if len(raw) == 0 {
		return nil, errors.New("couldn't read /proc/net")
	}
	owners := socketOwners()
	var out []portInfo
	for _, r := range raw {
		if r.proto != "TCP" {
			continue
		}
		out = append(out, portInfo{port: r.port, addr: r.addr, pid: owners[r.inode]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].port < out[j].port })
	return out, nil
}

// listenerPid: the process listening on a TCP port, 0 if none (or not visible to this user).
func listenerPid(port int) int {
	var inodes []uint64
	for _, r := range procNetListen() {
		if r.proto == "TCP" && r.port == port {
			inodes = append(inodes, r.inode)
		}
	}
	if len(inodes) == 0 {
		return 0
	}
	owners := socketOwners()
	for _, ino := range inodes {
		if pid := owners[ino]; pid > 0 {
			return pid
		}
	}
	return 0
}
