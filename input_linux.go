//go:build linux

package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Mouse, keyboard and screen pixels on Linux. X11 sessions (Ubuntu on Xorg) use xdotool and
// ImageMagick; Wayland sessions use ydotool (its daemon must run) and whatever screenshot
// tool the desktop allows. Windows virtual-key codes stay the common currency.

const inputSupported = true

var errNoInput = errors.New("window, screen and input control needs a desktop session")

var displayOnce sync.Once

// ensureDisplayEnv finds the desktop session when TokenSaver was started without one
// (systemd service, ssh): the user's Wayland socket or X display and its Xauthority.
func ensureDisplayEnv() {
	displayOnce.Do(func() {
		run := os.Getenv("XDG_RUNTIME_DIR")
		if run == "" {
			run = fmt.Sprintf("/run/user/%d", os.Getuid())
			os.Setenv("XDG_RUNTIME_DIR", run)
		}
		if os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == "" {
			if m, _ := filepath.Glob(filepath.Join(run, "wayland-[0-9]")); len(m) > 0 {
				os.Setenv("WAYLAND_DISPLAY", filepath.Base(m[0]))
				os.Setenv("XDG_SESSION_TYPE", "wayland")
			}
		}
		if os.Getenv("DISPLAY") == "" {
			if m, _ := filepath.Glob("/tmp/.X11-unix/X[0-9]*"); len(m) > 0 {
				os.Setenv("DISPLAY", ":"+strings.TrimPrefix(filepath.Base(m[0]), "X"))
			}
		}
		if os.Getenv("XAUTHORITY") == "" {
			h, _ := os.UserHomeDir()
			cands, _ := filepath.Glob(filepath.Join(run, ".mutter-Xwaylandauth.*"))
			cands = append(cands, filepath.Join(run, "gdm", "Xauthority"), filepath.Join(h, ".Xauthority"))
			for _, c := range cands {
				if exists(c) {
					os.Setenv("XAUTHORITY", c)
					break
				}
			}
		}
		if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && exists(filepath.Join(run, "bus")) {
			os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(run, "bus"))
		}
	})
}

// onWayland: a Wayland session (Ubuntu's default). XWayland apps are still reachable
// through DISPLAY, but native windows only through Wayland-aware tools.
func onWayland() bool {
	ensureDisplayEnv()
	if os.Getenv("TOKENSAVER_X11") != "" {
		return false
	}
	return os.Getenv("WAYLAND_DISPLAY") != "" || strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland")
}

func hasDesktop() bool {
	ensureDisplayEnv()
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func needTool(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s isn't installed (%s)", name, installHint(name))
	}
	return nil
}

func runTool(name string, args ...string) (string, error) {
	if !hasDesktop() {
		return "", errNoInput
	}
	if err := needTool(name); err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	c := exec.Command(name, args...)
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return string(out), fmt.Errorf("%s: %v %s", name, err, clip(strings.TrimSpace(stderr.String()), 200))
	}
	return string(out), nil
}

func xdo(args ...string) (string, error) { return runTool("xdotool", args...) }

// ydo runs ydotool (1.x syntax); its daemon ydotoold needs access to /dev/uinput.
func ydo(args ...string) error {
	_, err := runTool("ydotool", args...)
	if err != nil && strings.Contains(err.Error(), "socket") {
		return errors.New("ydotoold isn't running (Wayland input needs it; or log in with \"Ubuntu on Xorg\")")
	}
	return err
}

func dpiAware() {}

var lastCursor struct {
	sync.Mutex
	x, y int
}

func cursorPos() (int, int) {
	if !onWayland() {
		if out, err := xdo("getmouselocation", "--shell"); err == nil {
			var x, y int
			for _, l := range strings.Split(out, "\n") {
				if v, ok := strings.CutPrefix(l, "X="); ok {
					x, _ = strconv.Atoi(v)
				}
				if v, ok := strings.CutPrefix(l, "Y="); ok {
					y, _ = strconv.Atoi(v)
				}
			}
			return x, y
		}
	}
	lastCursor.Lock()
	defer lastCursor.Unlock()
	return lastCursor.x, lastCursor.y
}

func setCursor(x, y int) {
	lastCursor.Lock()
	lastCursor.x, lastCursor.y = x, y
	lastCursor.Unlock()
	if onWayland() {
		ydo("mousemove", "--absolute", "-x", strconv.Itoa(x), "-y", strconv.Itoa(y))
		return
	}
	xdo("mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y))
}

var xButtons = map[string]string{"left": "1", "middle": "2", "right": "3", "x1": "8", "x2": "9"}

// ydotool button numbers: 0 left, 1 right, 2 middle, 3 side, 4 extra; 0x40 = down, 0x80 = up.
var yButtons = map[string]int{"left": 0, "right": 1, "middle": 2, "x1": 3, "x2": 4}

func mouseButton(button string, down bool) error {
	b := strings.ToLower(button)
	if b == "" {
		b = "left"
	}
	if onWayland() {
		n, ok := yButtons[b]
		if !ok {
			return fmt.Errorf("unknown mouse button %q", button)
		}
		code := n | 0x80
		if down {
			code = n | 0x40
		}
		return ydo("click", fmt.Sprintf("0x%02X", code))
	}
	n, ok := xButtons[b]
	if !ok {
		return fmt.Errorf("unknown mouse button %q", button)
	}
	act := "mouseup"
	if down {
		act = "mousedown"
	}
	_, err := xdo(act, n)
	return err
}

// mouseWheel scrolls notches (positive = up, like Windows).
func mouseWheel(notches int) {
	if notches == 0 {
		return
	}
	if onWayland() {
		ydo("mousemove", "--wheel", "-x", "0", "-y", strconv.Itoa(notches))
		return
	}
	btn, n := "4", notches
	if n < 0 {
		btn, n = "5", -n
	}
	xdo("click", "--repeat", strconv.Itoa(n), "--delay", "15", btn)
}

var vkKeysym = map[uint16]string{
	0x08: "BackSpace", 0x09: "Tab", 0x0D: "Return", 0x10: "Shift_L", 0x11: "Control_L", 0x12: "Alt_L", 0x13: "Pause",
	0x14: "Caps_Lock", 0x1B: "Escape", 0x20: "space", 0x21: "Prior", 0x22: "Next", 0x23: "End", 0x24: "Home",
	0x25: "Left", 0x26: "Up", 0x27: "Right", 0x28: "Down", 0x2C: "Print", 0x2D: "Insert", 0x2E: "Delete",
	0x5B: "Super_L", 0x5C: "Super_R", 0x5D: "Menu", 0x6A: "KP_Multiply", 0x6B: "KP_Add", 0x6D: "KP_Subtract",
	0x6E: "KP_Decimal", 0x6F: "KP_Divide", 0x90: "Num_Lock", 0x91: "Scroll_Lock", 0xA0: "Shift_L", 0xA1: "Shift_R",
	0xA2: "Control_L", 0xA3: "Control_R", 0xA4: "Alt_L", 0xA5: "Alt_R", 0xAD: "XF86AudioMute",
	0xAE: "XF86AudioLowerVolume", 0xAF: "XF86AudioRaiseVolume", 0xB0: "XF86AudioNext", 0xB1: "XF86AudioPrev",
	0xB2: "XF86AudioStop", 0xB3: "XF86AudioPlay", 0xBA: "semicolon", 0xBB: "equal", 0xBC: "comma", 0xBD: "minus",
	0xBE: "period", 0xBF: "slash", 0xC0: "grave", 0xDB: "bracketleft", 0xDC: "backslash", 0xDD: "bracketright",
	0xDE: "apostrophe",
}

// vkEvdev: Linux input key codes for ydotool.
var vkEvdev = map[uint16]int{
	0x08: 14, 0x09: 15, 0x0D: 28, 0x10: 42, 0x11: 29, 0x12: 56, 0x13: 119, 0x14: 58, 0x1B: 1, 0x20: 57,
	0x21: 104, 0x22: 109, 0x23: 107, 0x24: 102, 0x25: 105, 0x26: 103, 0x27: 106, 0x28: 108, 0x2C: 99,
	0x2D: 110, 0x2E: 111, 0x5B: 125, 0x5C: 126, 0x5D: 127, 0x6A: 55, 0x6B: 78, 0x6D: 74, 0x6E: 83, 0x6F: 98,
	0x90: 69, 0x91: 70, 0xA0: 42, 0xA1: 54, 0xA2: 29, 0xA3: 97, 0xA4: 56, 0xA5: 100, 0xAD: 113, 0xAE: 114,
	0xAF: 115, 0xB0: 163, 0xB1: 165, 0xB2: 166, 0xB3: 164, 0xBA: 39, 0xBB: 13, 0xBC: 51, 0xBD: 12, 0xBE: 52,
	0xBF: 53, 0xC0: 41, 0xDB: 26, 0xDC: 43, 0xDD: 27, 0xDE: 40,
	0x30: 11, 0x31: 2, 0x32: 3, 0x33: 4, 0x34: 5, 0x35: 6, 0x36: 7, 0x37: 8, 0x38: 9, 0x39: 10,
	0x60: 82, 0x61: 79, 0x62: 80, 0x63: 81, 0x64: 75, 0x65: 76, 0x66: 77, 0x67: 71, 0x68: 72, 0x69: 73,
}

func init() {
	for c := uint16('A'); c <= 'Z'; c++ {
		vkKeysym[c] = strings.ToLower(string(rune(c)))
	}
	for c := uint16('0'); c <= '9'; c++ {
		vkKeysym[c] = string(rune(c))
	}
	for i := uint16(1); i <= 24; i++ {
		vkKeysym[0x6F+i] = "F" + strconv.Itoa(int(i))
	}
	for i := uint16(0); i <= 9; i++ {
		vkKeysym[0x60+i] = "KP_" + strconv.Itoa(int(i))
	}
	letters := "qwertyuiop" + "asdfghjkl" + "zxcvbnm"
	codes := []int{16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 30, 31, 32, 33, 34, 35, 36, 37, 38, 44, 45, 46, 47, 48, 49, 50}
	for i, c := range letters {
		vkEvdev[uint16(c-'a'+'A')] = codes[i]
	}
	fcodes := []int{59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 87, 88}
	for i, c := range fcodes {
		vkEvdev[uint16(0x70+i)] = c
	}
	for i := 0; i < 12; i++ {
		vkEvdev[uint16(0x7C+i)] = 183 + i // F13-F24
	}
}

func sendKey(vk uint16, up bool) {
	if onWayland() {
		code, ok := vkEvdev[vk]
		if !ok {
			return
		}
		state := "1"
		if up {
			state = "0"
		}
		ydo("key", fmt.Sprintf("%d:%s", code, state))
		return
	}
	sym, ok := vkKeysym[vk]
	if !ok {
		return
	}
	act := "keydown"
	if up {
		act = "keyup"
	}
	xdo(act, sym)
}

func typeText(s string) {
	if s == "" {
		return
	}
	if onWayland() {
		ydo("type", "--", s)
		return
	}
	xdo("type", "--clearmodifiers", "--delay", "6", "--", s)
}

// keyHeld can't be polled without a keyboard hook here; the terminal's hold-key gestures don't apply.
func keyHeld(vk uint16) bool { return false }

func recordInput(stopVK uint16) ([]rawInput, error) {
	return nil, errors.New("recording macros isn't available on Linux yet; write the macro steps instead")
}

// grabArea returns the screen pixels of a rectangle as BGRA, like the Windows version.
func grabArea(x, y, w, h int) ([]byte, error) {
	img, err := screenRegionPNG(x, y, w, h)
	if err != nil {
		return nil, err
	}
	return pngToBGRA(img, w, h)
}

func pngToBGRA(b []byte, w, h int) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	px := rgba.Pix
	for i := 0; i+3 < len(px); i += 4 {
		px[i], px[i+2] = px[i+2], px[i]
	}
	return px, nil
}

// screenRegionPNG captures part of the screen as PNG.
func screenRegionPNG(x, y, w, h int) ([]byte, error) {
	if !hasDesktop() {
		return nil, errNoInput
	}
	if w <= 0 || h <= 0 {
		return nil, errors.New("empty area")
	}
	if !onWayland() {
		if _, err := exec.LookPath("import"); err == nil {
			out, err := exec.Command("import", "-silent", "-window", "root", "-crop", fmt.Sprintf("%dx%d+%d+%d", w, h, x, y), "+repage", "png:-").Output()
			if err == nil && len(out) > 0 {
				return rgbaPNG(out)
			}
		}
		if _, err := exec.LookPath("ffmpeg"); err == nil {
			out, err := exec.Command("ffmpeg", "-v", "error", "-f", "x11grab", "-video_size", fmt.Sprintf("%dx%d", w, h),
				"-i", fmt.Sprintf("%s+%d,%d", os.Getenv("DISPLAY"), x, y), "-frames:v", "1", "-f", "image2pipe", "-vcodec", "png", "-").Output()
			if err == nil && len(out) > 0 {
				return rgbaPNG(out)
			}
		}
		return nil, errors.New("screen capture needs ImageMagick (" + installHint("magick") + ")")
	}
	full, err := waylandScreenshot()
	if err != nil {
		return nil, err
	}
	return cropPNG(full, x, y, w, h)
}

// rgbaPNG re-encodes any PNG as 8-bit RGBA, the layout the Windows captures have.
func rgbaPNG(b []byte) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if _, ok := src.(*image.RGBA); ok {
		return b, nil
	}
	dst := image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func cropPNG(b []byte, x, y, w, h int) ([]byte, error) {
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	r := image.Rect(x, y, x+w, y+h).Intersect(src.Bounds())
	if r.Empty() {
		return nil, errors.New("the area is off screen")
	}
	dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), src, r.Min, draw.Src)
	var out bytes.Buffer
	png.Encode(&out, dst)
	return out.Bytes(), nil
}

// waylandScreenshot: grim (wlroots desktops), spectacle (KDE) or gnome-screenshot. GNOME
// limits silent screenshots to its own tools; when none works, "Ubuntu on Xorg" does.
func waylandScreenshot() ([]byte, error) {
	dir, err := os.MkdirTemp(filepath.Join(appHome), "shot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "s.png")
	tries := [][]string{{"grim", f}, {"spectacle", "-b", "-n", "-f", "-o", f}, {"gnome-screenshot", "-f", f}}
	for _, t := range tries {
		if _, err := exec.LookPath(t[0]); err != nil {
			continue
		}
		if exec.Command(t[0], t[1:]...).Run() == nil && exists(f) {
			return os.ReadFile(f)
		}
	}
	return nil, errors.New("this Wayland desktop doesn't allow silent screenshots (install grim on wlroots desktops, or log in with \"Ubuntu on Xorg\")")
}
