//go:build linux

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Windows on Linux: wmctrl/xdotool on X11, the accessibility bus on Wayland (where apps
// can't see or move other apps' windows; the user brings a window to the front).

func listWindowsRaw() []winInfo {
	if !hasDesktop() {
		return nil
	}
	if onWayland() {
		return atspiWindows()
	}
	if _, err := exec.LookPath("wmctrl"); err == nil {
		if ws := wmctrlWindows(); ws != nil {
			return ws
		}
	}
	return xdotoolWindows()
}

// wmctrlWindows parses "wmctrl -lpG": id desktop pid x y w h host title.
func wmctrlWindows() []winInfo {
	out, err := exec.Command("wmctrl", "-lpG").Output()
	if err != nil {
		return nil
	}
	hidden := hiddenWindows()
	var ws []winInfo
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(l)
		if len(f) < 8 || f[1] == "-1" { // -1: desktop and panels
			continue
		}
		id, err := strconv.ParseUint(strings.TrimPrefix(f[0], "0x"), 16, 64)
		if err != nil {
			continue
		}
		pid, _ := strconv.Atoi(f[2])
		x, _ := strconv.Atoi(f[3])
		y, _ := strconv.Atoi(f[4])
		w, _ := strconv.Atoi(f[5])
		h, _ := strconv.Atoi(f[6])
		title := strings.Join(f[8:], " ")
		ws = append(ws, winInfo{hwnd: uintptr(id), title: title, pid: pid, exe: exeName(pid), x: x, y: y, w: w, h: h, minimized: hidden[uintptr(id)]})
	}
	return ws
}

// hiddenWindows: windows with _NET_WM_STATE_HIDDEN (minimized), via one xdotool search.
func hiddenWindows() map[uintptr]bool {
	m := map[uintptr]bool{}
	all, err := exec.Command("xdotool", "search", "--name", ".").Output()
	if err != nil {
		return m
	}
	vis, _ := exec.Command("xdotool", "search", "--onlyvisible", "--name", ".").Output()
	visible := map[string]bool{}
	for _, v := range strings.Fields(string(vis)) {
		visible[v] = true
	}
	for _, a := range strings.Fields(string(all)) {
		if !visible[a] {
			if id, err := strconv.ParseUint(a, 10, 64); err == nil {
				m[uintptr(id)] = true
			}
		}
	}
	return m
}

func xdotoolWindows() []winInfo {
	out, err := xdo("search", "--onlyvisible", "--name", ".")
	if err != nil {
		return nil
	}
	var ws []winInfo
	for _, s := range strings.Fields(out) {
		id, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			continue
		}
		w := winInfo{hwnd: uintptr(id)}
		if t, err := xdo("getwindowname", s); err == nil {
			w.title = strings.TrimSpace(t)
		}
		if p, err := xdo("getwindowpid", s); err == nil {
			w.pid, _ = strconv.Atoi(strings.TrimSpace(p))
			w.exe = exeName(w.pid)
		}
		if g, err := xdo("getwindowgeometry", "--shell", s); err == nil {
			for _, l := range strings.Split(g, "\n") {
				k, v, _ := strings.Cut(l, "=")
				n, _ := strconv.Atoi(v)
				switch k {
				case "X":
					w.x = n
				case "Y":
					w.y = n
				case "WIDTH":
					w.w = n
				case "HEIGHT":
					w.h = n
				}
			}
		}
		if w.title != "" && w.w > 1 && w.h > 1 {
			ws = append(ws, w)
		}
	}
	return ws
}

func foregroundHwnd() uintptr {
	if !hasDesktop() {
		return 0
	}
	if onWayland() {
		atspiWindows()
		return atspiActive
	}
	out, err := xdo("getactivewindow")
	if err != nil {
		return 0
	}
	id, _ := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	return uintptr(id)
}

// captureWindow: the window's pixels as PNG.
func captureWindow(w winInfo) ([]byte, error) {
	if !hasDesktop() {
		return nil, errNoInput
	}
	if !onWayland() && !isAtspiHandle(w.hwnd) {
		if _, err := exec.LookPath("import"); err == nil {
			out, err := exec.Command("import", "-silent", "-window", fmt.Sprint(uint64(w.hwnd)), "png:-").Output()
			if err == nil && len(out) > 0 {
				return rgbaPNG(out)
			}
		}
	}
	if w.w <= 0 || w.h <= 0 {
		return nil, errors.New("this window's position isn't known here (Wayland); take a screen capture instead")
	}
	return screenRegionPNG(w.x, w.y, w.w, w.h)
}

func focusWindow(w winInfo) error {
	if !hasDesktop() {
		return errNoInput
	}
	if onWayland() || isAtspiHandle(w.hwnd) {
		if foregroundHwnd() == w.hwnd || atspiActivePid() == w.pid && w.pid != 0 {
			return nil
		}
		return fmt.Errorf("Wayland doesn't let apps switch windows; bring %q to the front, or log in with \"Ubuntu on Xorg\"", w.title)
	}
	if _, err := exec.LookPath("wmctrl"); err == nil {
		exec.Command("wmctrl", "-ia", fmt.Sprintf("0x%08x", uint64(w.hwnd))).Run()
	} else {
		xdo("windowactivate", fmt.Sprint(uint64(w.hwnd)))
	}
	for i := 0; i < 20; i++ {
		if foregroundHwnd() == w.hwnd {
			time.Sleep(60 * time.Millisecond)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("couldn't bring %q to the front", w.title)
}

func atspiActivePid() int {
	for _, w := range atspiWindows() {
		if w.hwnd == atspiActive {
			return w.pid
		}
	}
	return 0
}

var terminalApps = map[string]bool{"gnome-terminal-server": true, "gnome-terminal": true, "kgx": true, "ptyxis": true,
	"konsole": true, "xfce4-terminal": true, "tilix": true, "terminator": true, "alacritty": true, "kitty": true,
	"wezterm-gui": true, "xterm": true, "foot": true, "mate-terminal": true, "lxterminal": true, "qterminal": true}

func activeIsTerminal() bool {
	fg := foregroundHwnd()
	for _, w := range listWindowsRaw() {
		if w.hwnd == fg {
			return terminalApps[strings.ToLower(w.exe)]
		}
	}
	return false
}

// copyWindowText focuses the window, selects all and copies, then puts the old clipboard back.
func copyWindowText(w winInfo) (string, error) {
	prev, hadText := clipText()
	prevFg := foregroundHwnd()
	if err := focusWindow(w); err != nil {
		return "", err
	}
	marker := "tokensaver-copy-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	setClipText(marker)
	pressCombo("ctrl+a")
	time.Sleep(80 * time.Millisecond)
	if terminalApps[strings.ToLower(w.exe)] {
		pressCombo("ctrl+shift+c")
	} else {
		pressCombo("ctrl+c")
	}
	got := ""
	for i := 0; i < 40; i++ {
		time.Sleep(25 * time.Millisecond)
		if t, ok := clipText(); ok && t != marker {
			got = t
			break
		}
	}
	pressCombo("right") // drop the selection
	if hadText {
		setClipText(prev)
	}
	if prevFg != 0 && prevFg != w.hwnd && !onWayland() {
		exec.Command("xdotool", "windowactivate", fmt.Sprint(uint64(prevFg))).Run()
	}
	if strings.TrimSpace(got) == "" {
		return "", errors.New("nothing was copied (the window may not allow selecting its text)")
	}
	return got, nil
}

// pasteText puts text in through the clipboard (fast and exact), then restores the clipboard text.
func pasteText(s string) error {
	if activeIsTerminal() || len(s) <= 64 && !strings.ContainsAny(s, "\n\t") {
		// terminals disagree on paste keys (xterm reads Ctrl+Shift+V as "literal next"); typing is safe there
		typeText(s)
		return nil
	}
	prev, had := clipText()
	if err := setClipText(s); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	pressCombo("ctrl+v")
	time.Sleep(150 * time.Millisecond)
	if had {
		setClipText(prev)
	}
	return nil
}

func clipText() (string, bool) {
	if !hasDesktop() {
		return "", false
	}
	var c *exec.Cmd
	switch {
	case onWayland() && hasCmd("wl-paste"):
		c = exec.Command("wl-paste", "--no-newline", "--type", "text")
	case hasCmd("xclip"):
		c = exec.Command("xclip", "-selection", "clipboard", "-o")
	case hasCmd("xsel"):
		c = exec.Command("xsel", "--clipboard", "--output")
	default:
		return "", false
	}
	out, err := c.Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

func setClipText(s string) error {
	if !hasDesktop() {
		return errNoInput
	}
	var c *exec.Cmd
	switch {
	case onWayland() && hasCmd("wl-copy"):
		c = exec.Command("wl-copy", "--type", "text/plain;charset=utf-8")
	case hasCmd("xclip"):
		c = exec.Command("xclip", "-selection", "clipboard", "-i")
	case hasCmd("xsel"):
		c = exec.Command("xsel", "--clipboard", "--input")
	default:
		if onWayland() {
			return errors.New("the clipboard needs wl-clipboard (" + installHint("wl-copy") + ")")
		}
		return errors.New("the clipboard needs xclip (" + installHint("xclip") + ")")
	}
	c.Stdin = strings.NewReader(s) // the tool keeps serving the text in the background
	return c.Run()
}

func hasCmd(n string) bool {
	_, err := exec.LookPath(n)
	return err == nil
}
