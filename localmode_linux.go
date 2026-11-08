//go:build linux

package main

// Pieces of ContextSaver for Linux that local mode needs and TokenSaver's Linux code
// doesn't have under these names.

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var _ = fmt.Sprint
var _ = filepath.Join
var _ = bufio.NewScanner
var _ = strconv.Itoa

func init() {
	mapWinPath = func(p string) (string, error) {
		if !isWinPath(p) {
			return p, nil
		}
		return winToLinux(p)
	}
}

// TokenSaver keeps repo copies current by itself (repos.go), so local mode has no
// update questions or notes to add.
func repoUpdateNotes(c string, a map[string]any) string { return "" }
func shortRepoPaths(s string) string                    { return s }
func shortRepoErr(err error) error                      { return err }
func reposToAsk(c string, a map[string]any, settled map[string]bool) []repoSpec { return nil }
func repoUpdatedAgo(s repoSpec) string                  { return "" }
func updateRepoCopy(owner, name string) (string, error) {
	return "", errors.New("repo copies update by themselves now")
}

var _ = os.Getpid
var _ = strings.TrimSpace

// addrIn: the host and port a start script or a model server's command line uses
// (llama-server's own defaults when it names none).
func addrIn(s string) (string, int) {
	host, port := "127.0.0.1", 8080
	if m := portRe.FindStringSubmatch(s); m != nil {
		port, _ = strconv.Atoi(m[1])
	} else if m := setPortRe.FindStringSubmatch(s); m != nil {
		port, _ = strconv.Atoi(m[1])
	}
	if m := hostRe.FindStringSubmatch(s); m != nil && m[1] != "0.0.0.0" && !strings.Contains(m[1], "%") {
		host = m[1]
	}
	return host, port
}

// pidOnPort: the process listening on TCP port, 0 if none (or not visible to this user).
func pidOnPort(port int) int {
	inodes := listenInodes(port)
	for _, pid := range inodeOwners(inodes) {
		if pid > 0 {
			return pid
		}
	}
	return 0
}

// listenInodes: socket inodes listening on TCP port (IPv4 and IPv6).
func listenInodes(port int) map[uint64]bool {
	out := map[uint64]bool{}
	for _, l := range tcpListeners() {
		if l.port == port {
			out[l.inode] = true
		}
	}
	return out
}

// inodeOwners maps the given socket inodes to the pid holding them. Without root only
// this user's processes are visible, which covers every server this app starts.
func inodeOwners(want map[uint64]bool) map[uint64]int {
	owners := map[uint64]int{}
	if len(want) == 0 {
		return owners
	}
	dirs, _ := os.ReadDir("/proc")
	for _, d := range dirs {
		pid, err := strconv.Atoi(d.Name())
		if err != nil {
			continue
		}
		fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			continue
		}
		for _, fd := range fds {
			l, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
			if err != nil || !strings.HasPrefix(l, "socket:[") {
				continue
			}
			ino, err := strconv.ParseUint(l[8:len(l)-1], 10, 64)
			if err != nil || !want[ino] {
				continue
			}
			if _, seen := owners[ino]; !seen {
				owners[ino] = pid
			}
			if len(owners) == len(want) {
				return owners
			}
		}
	}
	return owners
}

type diskMount struct{ dev, dir, fs string }

// diskMounts: real disks (ext4, btrfs, ntfs, vfat...), not snaps, loops or pseudo filesystems.
func diskMounts() []diskMount {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return []diskMount{{"", "/", ""}}
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []diskMount
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		x := strings.Fields(sc.Text())
		if len(x) < 3 || !strings.HasPrefix(x[0], "/dev/") || strings.HasPrefix(x[0], "/dev/loop") || x[2] == "squashfs" {
			continue
		}
		dir := unescapeMount(x[1])
		if seen[x[0]] || strings.HasPrefix(dir, "/snap/") || dir == "/boot/efi" {
			continue
		}
		seen[x[0]] = true
		out = append(out, diskMount{x[0], dir, x[2]})
	}
	if len(out) == 0 {
		out = []diskMount{{"", "/", ""}}
	}
	return out
}

// unescapeMount turns /proc/mounts' \040 (space) and friends back into characters.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// tcpListeners reads /proc/net/tcp and tcp6 for sockets in LISTEN state.
func tcpListeners() []tcpListen {
	var out []tcpListen
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			x := strings.Fields(line)
			if i == 0 || len(x) < 10 || x[3] != "0A" { // 0A = LISTEN
				continue
			}
			addr, port := hexAddr(x[1])
			ino, _ := strconv.ParseUint(x[9], 10, 64)
			out = append(out, tcpListen{addr, port, ino})
		}
	}
	return out
}

type tcpListen struct {
	addr  string
	port  int
	inode uint64
}

// hexAddr decodes /proc/net's "0100007F:1F90" into ("127.0.0.1", 8080).
func hexAddr(s string) (string, int) {
	h, p, ok := strings.Cut(s, ":")
	if !ok {
		return "", 0
	}
	port, _ := strconv.ParseUint(p, 16, 16)
	if len(h) == 8 {
		v, _ := strconv.ParseUint(h, 16, 32)
		return fmt.Sprintf("%d.%d.%d.%d", v&0xff, v>>8&0xff, v>>16&0xff, v>>24), int(port)
	}
	switch h {
	case strings.Repeat("0", 32):
		return "::", int(port)
	case "00000000000000000000000001000000":
		return "::1", int(port)
	}
	return "[" + strings.ToLower(h) + "]", int(port)
}
