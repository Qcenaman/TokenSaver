//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// AT-SPI is the Linux accessibility bus (what screen readers use), the counterpart of
// Windows UI Automation. A small Python script (python3-gi, part of Ubuntu desktop) lists
// windows, reads a window's text in the same TSV the Windows script prints, and clicks
// or fills controls by name. It works on X11 and on Wayland.

const atspiScript = `import sys
import warnings
warnings.simplefilter('ignore')
import gi
gi.require_version('Atspi', '2.0')
from gi.repository import Atspi

KIND = {
    'push button': 'Button', 'toggle button': 'Button', 'menu item': 'MenuItem', 'check menu item': 'MenuItem',
    'radio menu item': 'MenuItem', 'check box': 'CheckBox', 'radio button': 'RadioButton', 'page tab': 'TabItem',
    'link': 'Hyperlink', 'text': 'Edit', 'entry': 'Edit', 'password text': 'Edit', 'spin button': 'Edit',
    'label': 'Text', 'static': 'Text', 'heading': 'Text', 'paragraph': 'Text', 'caption': 'Text',
    'list item': 'ListItem', 'table cell': 'DataItem', 'tree item': 'TreeItem', 'combo box': 'ComboBox',
    'frame': 'Window', 'window': 'Window', 'dialog': 'Window', 'panel': 'Pane', 'filler': 'Pane',
    'scroll pane': 'Pane', 'viewport': 'Pane', 'section': 'Group', 'list': 'List', 'tree': 'Tree',
    'table': 'Table', 'tool bar': 'ToolBar', 'menu bar': 'MenuBar', 'page tab list': 'Tab', 'scroll bar': 'ScrollBar',
    'image': 'Image', 'icon': 'Image', 'separator': 'Separator', 'document web': 'Document', 'document frame': 'Document',
    'status bar': 'StatusBar', 'menu': 'Menu', 'slider': 'Slider', 'progress bar': 'ProgressBar',
}
TOP = ('frame', 'window', 'dialog', 'alert', 'file chooser')

def clean(s):
    return (s or '').replace('\t', ' ').replace('\r', ' ').replace('\n', ' ').strip()

def tops():
    d = Atspi.get_desktop(0)
    for i in range(d.get_child_count()):
        app = d.get_child_at_index(i)
        if app is None:
            continue
        try:
            pid = app.get_process_id()
        except Exception:
            pid = 0
        for j in range(app.get_child_count()):
            w = app.get_child_at_index(j)
            if w is None:
                continue
            try:
                if w.get_role_name() in TOP:
                    yield pid, app, w
            except Exception:
                pass

def ext(o):
    try:
        e = o.get_extents(Atspi.CoordType.SCREEN)
        return e.x, e.y, e.width, e.height
    except Exception:
        return 0, 0, 0, 0

def states(o):
    try:
        return o.get_state_set()
    except Exception:
        return None

def listing():
    for pid, app, w in tops():
        x, y, ww, hh = ext(w)
        st = states(w)
        active = 1 if st is not None and st.contains(Atspi.StateType.ACTIVE) else 0
        mini = 1 if st is not None and st.contains(Atspi.StateType.ICONIFIED) else 0
        print('W\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%s' % (pid, x, y, ww, hh, active, mini, clean(w.get_name())))

def pick(pid, title):
    best, bs = None, -1
    for p, app, w in tops():
        s = 0
        if pid and p == pid:
            s += 4
        n = clean(w.get_name())
        if title and n == title:
            s += 3
        elif title and title.lower() in n.lower():
            s += 1
        st = states(w)
        if st is not None and st.contains(Atspi.StateType.ACTIVE):
            s += 1
        if s > bs:
            best, bs = w, s
    if best is None or (pid and bs < 4 and not title):
        print('E no accessible window found (is the app running with accessibility on?)')
        sys.exit(1)
    return best

def ifaces(o):
    try:
        return set(o.get_interfaces())
    except Exception:
        return set()

def text_of(o):
    have = ifaces(o)
    if 'Text' in have:
        try:
            n = Atspi.Text.get_character_count(o)
            if n > 0:
                return clean(Atspi.Text.get_text(o, 0, min(n, 2000)))
        except Exception:
            pass
    if 'Value' in have:
        try:
            return clean('%g' % Atspi.Value.get_current_value(o))
        except Exception:
            pass
    return ''

def walk(o, depth, out, budget):
    if depth > 60 or budget[0] <= 0:
        return
    budget[0] -= 1
    try:
        role = o.get_role_name()
    except Exception:
        return
    st = states(o)
    if st is not None and not st.contains(Atspi.StateType.SHOWING) and role not in TOP:
        return
    kind = KIND.get(role, role.title().replace(' ', ''))
    x, y, w, h = ext(o)
    out.append((x, y, h, kind, clean(o.get_name()), text_of(o)))
    try:
        n = o.get_child_count()
    except Exception:
        n = 0
    for i in range(min(n, 500)):
        c = o.get_child_at_index(i)
        if c is not None:
            walk(c, depth + 1, out, budget)

def read(pid, title, maxn):
    w = pick(pid, title)
    out = []
    walk(w, 0, out, [maxn])
    for x, y, h, kind, name, val in out:
        print('%d\t%d\t%d\t%s\t%s\t%s' % (x, y, h, kind, name, val))

def do(pid, title, op, name, value):
    w = pick(pid, title)
    out = []
    def collect(o, depth):
        if depth > 60 or len(out) > 5000:
            return
        try:
            nm = clean(o.get_name()); role = o.get_role_name()
        except Exception:
            return
        out.append((o, nm, role))
        for i in range(min(o.get_child_count(), 500)):
            c = o.get_child_at_index(i)
            if c is not None:
                collect(c, depth + 1)
    collect(w, 0)
    q = name.lower()
    best, bs = None, 0
    for o, nm, role in out:
        l = nm.lower()
        s = 0
        if l == q:
            s = 4
        elif q and q in l:
            s = 2
        if s == 0:
            continue
        kind = KIND.get(role, '')
        if op == 'set' and kind == 'Edit':
            s += 2
        if op == 'click' and kind in ('Button', 'MenuItem', 'Hyperlink', 'TabItem', 'CheckBox', 'RadioButton', 'ListItem', 'TreeItem'):
            s += 2
        if s > bs:
            best, bs = o, s
    if best is None:
        print('E nothing called "%s" in that window' % name)
        sys.exit(1)
    x, y, ww, hh = ext(best)
    if op == 'set':
        try:
            if 'EditableText' in ifaces(best) and Atspi.EditableText.set_text_contents(best, value):
                print('OK set'); return
        except Exception:
            pass
        print('RECT %d %d %d %d' % (x, y, ww, hh)); return
    try:
        if 'Action' in ifaces(best) and Atspi.Action.get_n_actions(best) > 0:
            Atspi.Action.do_action(best, 0)
            print('OK invoked'); return
    except Exception:
        pass
    print('RECT %d %d %d %d' % (x, y, ww, hh))

try:
    mode = sys.argv[1]
    if mode == 'list':
        listing()
    elif mode == 'text':
        read(int(sys.argv[2] or 0), sys.argv[3], int(sys.argv[4]))
    elif mode == 'do':
        do(int(sys.argv[2] or 0), sys.argv[3], sys.argv[4], sys.argv[5], sys.argv[6])
except SystemExit:
    raise
except Exception as e:
    print('E ' + ' '.join(str(e).split()))
    sys.exit(1)
`

func atspiScriptPath() (string, error) {
	p := filepath.Join(appHome, "scripts", "atspi.py")
	if b, err := os.ReadFile(p); err == nil && string(b) == atspiScript {
		return p, nil
	}
	os.MkdirAll(filepath.Dir(p), 0o755)
	return p, os.WriteFile(p, []byte(atspiScript), 0o644)
}

// runAtspi runs the script and returns its output lines; "E ..." lines become errors.
func runAtspi(timeout time.Duration, args ...string) ([]string, error) {
	// The system Python has the desktop's GObject bindings; conda or pyenv ones usually don't.
	py := os.Getenv("TOKENSAVER_PYTHON")
	if py == "" {
		py = "/usr/bin/python3"
	}
	if !exists(py) {
		p, err := exec.LookPath("python3")
		if err != nil {
			return nil, errors.New("reading windows needs python3")
		}
		py = p
	}
	script, err := atspiScriptPath()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, py, append([]string{script}, args...)...).Output()
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "E ") {
			return nil, errors.New("accessibility: " + l[2:])
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("accessibility took too long")
		}
		if ee, ok := err.(*exec.ExitError); ok && strings.Contains(string(ee.Stderr), "Atspi") {
			return nil, errors.New("accessibility needs python3-gi and gir1.2-atspi-2.0 (" + installHint("python3-gi gir1.2-atspi-2.0") + ")")
		}
		return nil, fmt.Errorf("accessibility failed: %v", err)
	}
	return lines, nil
}

// atspiWindows lists top-level windows from the accessibility bus (works on Wayland).
func atspiWindows() []winInfo {
	lines, err := runAtspi(10*time.Second, "list")
	if err != nil {
		return nil
	}
	var ws []winInfo
	for i, l := range lines {
		f := strings.SplitN(l, "\t", 9)
		if len(f) < 9 || f[0] != "W" {
			continue
		}
		pid, _ := strconv.Atoi(f[1])
		x, _ := strconv.Atoi(f[2])
		y, _ := strconv.Atoi(f[3])
		w, _ := strconv.Atoi(f[4])
		h, _ := strconv.Atoi(f[5])
		wi := winInfo{hwnd: atspiHandle(pid, i), title: f[8], pid: pid, x: x, y: y, w: w, h: h, minimized: f[7] == "1"}
		wi.exe = exeName(pid)
		if f[6] == "1" {
			atspiActive = wi.hwnd
		}
		ws = append(ws, wi)
	}
	return ws
}

var atspiActive uintptr

// atspiHandle: Wayland windows have no id; pid and list position stand in for one.
func atspiHandle(pid, idx int) uintptr { return uintptr(1<<40 | pid<<12 | idx&0xfff) }

func isAtspiHandle(h uintptr) bool { return h>>40 == 1 }

func exeName(pid int) string {
	if e, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
		return filepath.Base(strings.TrimSuffix(e, " (deleted)"))
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); err == nil {
		return strings.TrimSpace(string(b))
	}
	return ""
}

// atspiTextLines returns the window's items in the Windows UIA script's TSV layout.
func atspiTextLines(w winInfo) ([]string, error) {
	return runAtspi(40*time.Second, "text", strconv.Itoa(w.pid), w.title, "4000")
}

func atspiDo(w winInfo, op, name, value string) (string, error) {
	lines, err := runAtspi(30*time.Second, "do", strconv.Itoa(w.pid), w.title, op, name, value)
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", errors.New("accessibility gave no answer")
	}
	return lines[0], nil
}
