package main

// The browser tool's eyes and hands, so canvas pages and games can be tested: screenshot and
// ocr of the page (the window tool's shrink, JPEG, OCR and local-AI question code, fed by
// Chrome's own capture), the page console, and mouse and keys by position sent to the page.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"regexp"
	"strings"
	"sync"
	"time"
)

// browserStep is the window tool's do step: same names and shapes (x,y are page pixels here).
type browserStep = uiStep

type browserVisionArgs struct {
	Action   string        `json:"action"`
	URL      string        `json:"url"`
	Show     bool          `json:"show"`
	X        *int          `json:"x"`
	Y        *int          `json:"y"`
	W        int           `json:"w"`
	H        int           `json:"h"`
	MaxPx    int           `json:"max_px"`
	MaxChars int           `json:"max_chars"`
	Question string        `json:"question"`
	GPU      string        `json:"gpu"`
	Force    bool          `json:"force"`
	Boxes    bool          `json:"boxes"`
	Changes  bool          `json:"changes"`
	Pattern  string        `json:"pattern"`
	Steps    []browserStep `json:"steps"`
	Then     string        `json:"then"`
	full     bool          // the call is from a full-output client (set from the marker)
}

var browserVisionActions = map[string]bool{"screenshot": true, "ocr": true, "console": true, "do": true}

// toolBrowserFull adds screenshot, ocr, console and do to the browser tool; the rest goes to toolBrowser.
func toolBrowserFull(raw json.RawMessage) (string, []toolImage, error) {
	var a browserVisionArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", nil, err
	}
	a.full = isFullCall(raw)
	a.Action = strings.ToLower(strings.TrimSpace(a.Action))
	if !browserVisionActions[a.Action] {
		s, err := toolBrowser(raw)
		return s, nil, err
	}
	if a.URL != "" { // open the page first, as the text actions do
		if _, err := toolBrowser(jsonRaw(map[string]any{"action": "open", "url": a.URL, "show": a.Show, "max_chars": 1})); err != nil {
			return "", nil, err
		}
	}
	browserMu.Lock()
	defer browserMu.Unlock()
	if err := ensureBrowser(false); err != nil {
		return "", nil, err
	}
	consoleWatchEnsure()
	w, err := pageConn()
	if err != nil {
		return "", nil, err
	}
	defer w.c.Close()
	maxc := outCap(isFullCall(raw), a.MaxChars, 4000, 30000)
	if a.MaxPx <= 0 && isFullCall(raw) {
		a.MaxPx = fullScreenshotPx
	}
	switch a.Action {
	case "screenshot":
		return pageScreenshot(w, a)
	case "ocr":
		s, err := pageOCR(w, a, maxc)
		return s, nil, err
	case "console":
		return consoleReport(a.Pattern, maxc), nil, nil
	}
	// do
	if len(a.Steps) == 0 {
		return "", nil, errors.New(`do needs steps, e.g. [{"hold":"ArrowRight","secs":2},{"press":"Space"},{"wait":1}]`)
	}
	var log []string
	for i, s := range a.Steps {
		r, err := pageStep(w, s)
		if err != nil {
			log = append(log, fmt.Sprintf("%d. failed: %v", i+1, err))
			break
		}
		log = append(log, fmt.Sprintf("%d. %s", i+1, r))
	}
	out := strings.Join(log, "\n")
	switch strings.ToLower(a.Then) {
	case "screenshot":
		a.X, a.Y = nil, nil
		s, imgs, err := pageScreenshot(w, a)
		if err != nil {
			return out + "\nscreenshot failed: " + err.Error(), nil, nil
		}
		return out + "\npage after:\n" + s, imgs, nil
	case "ocr":
		s, err := pageOCR(w, a, maxc)
		if err != nil {
			s = "ocr failed: " + err.Error()
		}
		return out + "\npage after:\n" + s, nil, nil
	case "text":
		t, _ := w.eval("document.body ? document.body.innerText : ''", 15*time.Second)
		body, note := capText(strings.TrimSpace(blankRe.ReplaceAllString(t, "\n\n")), maxc, "browser page")
		return out + "\npage after:\n" + body + note, nil, nil
	case "console":
		return out + "\n" + consoleReport(a.Pattern, maxc), nil, nil
	}
	return out, nil, nil
}

// ---------- capture ----------

type pageView struct{ w, h, dpr float64 }

func pageViewport(w *wsConn) pageView {
	s, _ := w.eval("JSON.stringify([innerWidth, innerHeight, devicePixelRatio || 1])", 5*time.Second)
	var v []float64
	if json.Unmarshal([]byte(s), &v) != nil || len(v) < 3 || v[0] <= 0 {
		return pageView{1280, 1600, 1}
	}
	return pageView{v[0], v[1], v[2]}
}

// pageCapture is a PNG of the page's visible area or of x,y,w,h (page CSS pixels), plus its origin and the device pixel ratio.
func pageCapture(w *wsConn, a browserVisionArgs) (img *image.RGBA, ox, oy int, v pageView, label string, err error) {
	v = pageViewport(w)
	p := map[string]any{"format": "png"}
	label = "page"
	if a.X != nil && a.Y != nil && a.W > 0 && a.H > 0 {
		p["clip"] = map[string]any{"x": *a.X, "y": *a.Y, "width": a.W, "height": a.H, "scale": 1}
		ox, oy = *a.X, *a.Y
		label = fmt.Sprintf("page area %d,%d %dx%d", *a.X, *a.Y, a.W, a.H)
	}
	res, err := w.call("Page.captureScreenshot", p, 20*time.Second)
	if err != nil {
		return nil, 0, 0, v, "", err
	}
	var r struct {
		Data string `json:"data"`
	}
	json.Unmarshal(res, &r)
	b, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil {
		return nil, 0, 0, v, "", err
	}
	src, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, 0, 0, v, "", err
	}
	rgba, ok := src.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(src.Bounds())
		draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
	}
	if t, e := w.eval("document.title + ' — ' + location.href", 5*time.Second); e == nil {
		label += " of " + clip(t, 80)
	}
	return rgba, ox, oy, v, label, nil
}

// last pictures and texts, for changes:true
var (
	pageLastMu   sync.Mutex
	pageLastSig  []byte
	pageLastText string
)

// thumbSig is a 32x32 grey thumbnail: enough to tell whether anything moved.
func thumbSig(img *image.RGBA) []byte {
	const n = 32
	b := img.Bounds()
	sig := make([]byte, n*n)
	for ty := 0; ty < n; ty++ {
		for tx := 0; tx < n; tx++ {
			x0, x1 := b.Min.X+tx*b.Dx()/n, b.Min.X+(tx+1)*b.Dx()/n
			y0, y1 := b.Min.Y+ty*b.Dy()/n, b.Min.Y+(ty+1)*b.Dy()/n
			var sum, cnt int
			for y := y0; y < y1; y += max(1, (y1-y0)/4) {
				for x := x0; x < x1; x += max(1, (x1-x0)/4) {
					o := img.PixOffset(x, y)
					sum += (int(img.Pix[o]) + int(img.Pix[o+1]) + int(img.Pix[o+2])) / 3
					cnt++
				}
			}
			if cnt > 0 {
				sig[ty*n+tx] = byte(sum / cnt)
			}
		}
	}
	return sig
}

// sigChange says how much of the picture changed between two thumbnails.
func sigChange(prev, cur []byte) string {
	if prev == nil || len(prev) != len(cur) {
		return "changes: no earlier picture to compare with (this one is kept for the next call)"
	}
	moved := 0
	for i := range cur {
		if absInt(int(cur[i])-int(prev[i])) > 6 {
			moved++
		}
	}
	if moved == 0 {
		return "changes: the picture is the same as last time"
	}
	return fmt.Sprintf("changes: the picture changed (%d%% of it)", max(1, moved*100/len(cur)))
}

func pageScreenshot(w *wsConn, a browserVisionArgs) (string, []toolImage, error) {
	img, _, _, v, label, err := pageCapture(w, a)
	if err != nil {
		return "", nil, err
	}
	maxPx := a.MaxPx
	if maxPx <= 0 {
		maxPx = 1000
	}
	maxPx = max(min(maxPx, 1600), 200)
	ow, oh := img.Bounds().Dx(), img.Bounds().Dy()
	small := shrink(img, maxPx)
	nw, nh := small.Bounds().Dx(), small.Bounds().Dy()
	var buf bytes.Buffer
	if err := jpegEncodeQ(&buf, small, 70); err != nil {
		return "", nil, err
	}
	cssW := float64(ow) / v.dpr
	info := fmt.Sprintf("%s: %dx%d shown at %dx%d", label, int(cssW), int(float64(oh)/v.dpr), nw, nh)
	if f := cssW / float64(nw); f > 1.01 || f < 0.99 {
		info += fmt.Sprintf("; multiply picture x,y by %.2f for click_at", f)
	}
	if a.X != nil && a.Y != nil && a.W > 0 {
		info += fmt.Sprintf(", then add %d,%d", *a.X, *a.Y)
	}
	if a.Changes {
		sig := thumbSig(img)
		pageLastMu.Lock()
		info += "\n" + sigChange(pageLastSig, sig)
		pageLastSig = sig
		pageLastMu.Unlock()
	}
	if strings.TrimSpace(a.Question) != "" {
		ans, err := askAboutImage(buf.Bytes(), "browser "+label, a.Question, gpuMode(a.GPU, a.Force))
		if err != nil {
			return "", nil, err
		}
		return info + ans, nil, nil
	}
	if a.full { // Perplexity may not see tool images: add them in words
		info += readableShot(buf.Bytes(), "browser "+label, gpuMode(a.GPU, a.Force))
	}
	return info, []toolImage{{data: buf.Bytes(), mime: "image/jpeg"}}, nil
}

// pageOCRPage reads the page (or area) by OCR, with word boxes in page pixels.
func pageOCRPage(w *wsConn, a browserVisionArgs) (pg ocrPage, ox, oy int, label string, err error) {
	img, ox, oy, v, label, err := pageCapture(w, a)
	if err != nil {
		return pg, 0, 0, "", err
	}
	var pb bytes.Buffer
	if err := png.Encode(&pb, img); err != nil {
		return pg, 0, 0, "", err
	}
	if pg, err = ocrPageOf(pb.Bytes()); err != nil {
		return pg, 0, 0, "", err
	}
	if v.dpr > 1.01 || v.dpr < 0.99 { // OCR sees device pixels; click_at takes page pixels
		for li := range pg.Lines {
			for wi := range pg.Lines[li].Words {
				wd := &pg.Lines[li].Words[wi]
				wd.X, wd.Y = int(float64(wd.X)/v.dpr), int(float64(wd.Y)/v.dpr)
				wd.W, wd.H = int(float64(wd.W)/v.dpr), int(float64(wd.H)/v.dpr)
			}
		}
	}
	return pg, ox, oy, label, nil
}

func pageOCR(w *wsConn, a browserVisionArgs, maxc int) (string, error) {
	pg, ox, oy, label, err := pageOCRPage(w, a)
	if err != nil {
		return "", err
	}
	text := pg.text()
	if a.Boxes {
		text = ocrWithPositions(pg, ox, oy)
		label += " (@ = page x,y for do click_at)"
	}
	head := label
	if a.Changes {
		pageLastMu.Lock()
		switch {
		case pageLastText == "":
			head += "\nchanges: no earlier text to compare with"
		case pageLastText == pg.text():
			head += "\nchanges: the text on the page is the same as last time"
		default:
			head += "\nchanges: the text on the page changed"
		}
		pageLastText = pg.text()
		pageLastMu.Unlock()
	}
	if strings.TrimSpace(text) == "" {
		return head + "\nno text found", nil
	}
	body, note := capText(text, maxc, "browser ocr")
	return head + "\n" + body + note, nil
}

// ---------- input ----------

type cdpKey struct {
	key, code string
	vk        int
	text      string
}

var cdpNamedKeys = map[string]cdpKey{
	"enter": {"Enter", "Enter", 13, "\r"}, "return": {"Enter", "Enter", 13, "\r"},
	"space": {" ", "Space", 32, " "}, "spacebar": {" ", "Space", 32, " "},
	"tab": {"Tab", "Tab", 9, ""}, "escape": {"Escape", "Escape", 27, ""}, "esc": {"Escape", "Escape", 27, ""},
	"backspace": {"Backspace", "Backspace", 8, ""}, "delete": {"Delete", "Delete", 46, ""}, "del": {"Delete", "Delete", 46, ""},
	"left": {"ArrowLeft", "ArrowLeft", 37, ""}, "arrowleft": {"ArrowLeft", "ArrowLeft", 37, ""},
	"up": {"ArrowUp", "ArrowUp", 38, ""}, "arrowup": {"ArrowUp", "ArrowUp", 38, ""},
	"right": {"ArrowRight", "ArrowRight", 39, ""}, "arrowright": {"ArrowRight", "ArrowRight", 39, ""},
	"down": {"ArrowDown", "ArrowDown", 40, ""}, "arrowdown": {"ArrowDown", "ArrowDown", 40, ""},
	"home": {"Home", "Home", 36, ""}, "end": {"End", "End", 35, ""}, "pageup": {"PageUp", "PageUp", 33, ""}, "pagedown": {"PageDown", "PageDown", 34, ""},
	"shift": {"Shift", "ShiftLeft", 16, ""}, "ctrl": {"Control", "ControlLeft", 17, ""}, "control": {"Control", "ControlLeft", 17, ""},
	"alt": {"Alt", "AltLeft", 18, ""}, "meta": {"Meta", "MetaLeft", 91, ""}, "win": {"Meta", "MetaLeft", 91, ""}, "cmd": {"Meta", "MetaLeft", 91, ""},
}

var cdpModBits = map[string]int{"Alt": 1, "Control": 2, "Meta": 4, "Shift": 8}

// parseCDPKey turns a key name (ArrowRight, right, Space, a, 7, F5) into the page's key event fields.
func parseCDPKey(name string) (cdpKey, error) {
	n := strings.TrimSpace(name)
	if k, ok := cdpNamedKeys[strings.ToLower(n)]; ok {
		return k, nil
	}
	if len(n) >= 2 && (n[0] == 'f' || n[0] == 'F') {
		var f int
		if _, err := fmt.Sscanf(n[1:], "%d", &f); err == nil && f >= 1 && f <= 12 {
			return cdpKey{fmt.Sprintf("F%d", f), fmt.Sprintf("F%d", f), 111 + f, ""}, nil
		}
	}
	if r := []rune(n); len(r) == 1 {
		c := r[0]
		switch {
		case c >= 'a' && c <= 'z':
			return cdpKey{string(c), "Key" + strings.ToUpper(string(c)), int(c - 32), string(c)}, nil
		case c >= 'A' && c <= 'Z':
			return cdpKey{string(c), "Key" + string(c), int(c), string(c)}, nil
		case c >= '0' && c <= '9':
			return cdpKey{string(c), "Digit" + string(c), int(c), string(c)}, nil
		default:
			return cdpKey{string(c), "", 0, string(c)}, nil
		}
	}
	return cdpKey{}, fmt.Errorf("unknown key %q (e.g. ArrowRight, Space, Enter, a, ctrl+a)", name)
}

func parseCDPCombo(s string) ([]cdpKey, error) {
	var ks []cdpKey
	for _, p := range strings.Split(s, "+") {
		if strings.TrimSpace(p) == "" { // "ctrl++" → the + key
			p = "+"
		}
		k, err := parseCDPKey(p)
		if err != nil {
			return nil, err
		}
		ks = append(ks, k)
	}
	if len(ks) == 0 {
		return nil, errors.New("no key given")
	}
	return ks, nil
}

func comboMods(ks []cdpKey) int {
	m := 0
	for _, k := range ks {
		m |= cdpModBits[k.key]
	}
	return m
}

func sendCDPKey(w *wsConn, k cdpKey, typ string, mods int, repeat bool) error {
	p := map[string]any{"type": typ, "key": k.key, "modifiers": mods}
	if k.code != "" {
		p["code"] = k.code
	}
	if k.vk != 0 {
		p["windowsVirtualKeyCode"], p["nativeVirtualKeyCode"] = k.vk, k.vk
	}
	if repeat {
		p["autoRepeat"] = true
	}
	if typ == "keyDown" && k.text != "" && mods&(2|4|1) == 0 { // printable keys type their text
		p["text"] = k.text
	}
	_, err := w.call("Input.dispatchKeyEvent", p, 5*time.Second)
	return err
}

func mouseEvent(w *wsConn, typ string, x, y int, button string, clicks int, extra map[string]any) error {
	p := map[string]any{"type": typ, "x": x, "y": y, "button": button, "clickCount": clicks}
	if typ == "mouseMoved" && button == "none" {
		p["buttons"] = 0
	}
	for k, v := range extra {
		p[k] = v
	}
	_, err := w.call("Input.dispatchMouseEvent", p, 5*time.Second)
	return err
}

func mouseButtonName(b string) (string, int) {
	switch strings.ToLower(b) {
	case "right":
		return "right", 1
	case "middle":
		return "middle", 1
	case "double":
		return "left", 2
	}
	return "left", 1
}

func pagePoint(p []int) (int, int, error) {
	if len(p) < 2 {
		return 0, 0, errors.New("give a position as [x, y] in page pixels")
	}
	return p[0], p[1], nil
}

// pageStep runs one do step against the page.
func pageStep(w *wsConn, s browserStep) (string, error) {
	switch {
	case s.Click != "":
		r, err := w.eval(jsClickLabel(s.Click), 10*time.Second)
		if err != nil {
			return "", err
		}
		if r == "NOTFOUND" {
			return "", fmt.Errorf("nothing clickable called %q on the page (click_text or click_at for canvas)", s.Click)
		}
		time.Sleep(300 * time.Millisecond)
		return fmt.Sprintf("clicked %q", r), nil
	case s.ClickText != "":
		return pageClickText(w, s)
	case len(s.ClickAt) > 0 || s.Scroll != 0 || len(s.Drag) > 0 || len(s.Move) > 0:
		return pageMouseStep(w, s)
	case s.Hold != "":
		return pageHold(w, s.Hold, s.Secs)
	case s.Press != "":
		return pagePress(w, s.Press)
	case s.Type != "":
		if _, err := w.call("Input.insertText", map[string]any{"text": s.Type}, 5*time.Second); err != nil {
			return "", err
		}
		return fmt.Sprintf("typed %d characters", len([]rune(s.Type))), nil
	case s.Wait > 0:
		time.Sleep(time.Duration(min(s.Wait, 30) * float64(time.Second)))
		return fmt.Sprintf("waited %gs", s.Wait), nil
	}
	return "", errors.New("empty step (click, click_text, click_at, move, drag, scroll, hold, press, type or wait)")
}

// pageClickText clicks text found on the page by OCR (canvas text too).
func pageClickText(w *wsConn, s browserStep) (string, error) {
	pg, _, _, _, err := pageOCRPage(w, browserVisionArgs{})
	if err != nil {
		return "", err
	}
	x, y, what, err := findOCRText(pg, s.ClickText, s.Nth)
	if err != nil {
		return "", err
	}
	btn, n := mouseButtonName(s.Button)
	if err := pageClick(w, x, y, btn, n); err != nil {
		return "", err
	}
	return fmt.Sprintf("clicked %q at %d,%d", what, x, y), nil
}

// pageMouseStep: click_at, scroll, drag and move.
func pageMouseStep(w *wsConn, s browserStep) (string, error) {
	btn, n := mouseButtonName(s.Button)
	switch {
	case len(s.ClickAt) > 0:
		x, y, err := pagePoint(s.ClickAt)
		if err != nil {
			return "", err
		}
		if err := pageClick(w, x, y, btn, n); err != nil {
			return "", err
		}
		return fmt.Sprintf("%s click at %d,%d", strings.ToLower(orDefault(s.Button, "left")), x, y), nil
	case s.Scroll != 0:
		x, y := 640, 400
		if len(s.At) >= 2 {
			x, y = s.At[0], s.At[1]
		} else if v := pageViewport(w); v.w > 0 {
			x, y = int(v.w/2), int(v.h/2)
		}
		k := max(min(s.Scroll, 50), -50)
		// +up / -down notches, as in the window tool; one notch is 100 page pixels
		if err := mouseEvent(w, "mouseWheel", x, y, "none", 0, map[string]any{"deltaX": 0, "deltaY": -100 * k}); err != nil {
			return "", err
		}
		time.Sleep(200 * time.Millisecond)
		dir := "up"
		if k < 0 {
			dir = "down"
		}
		return fmt.Sprintf("scrolled %s %d", dir, absInt(k)), nil
	case len(s.Drag) > 0:
		return pageDrag(w, s.Drag, btn)
	}
	x, y, err := pagePoint(s.Move)
	if err != nil {
		return "", err
	}
	if err := mouseEvent(w, "mouseMoved", x, y, "none", 0, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("pointer at %d,%d", x, y), nil
}

func pageDrag(w *wsConn, d []int, btn string) (string, error) {
	if len(d) < 4 {
		return "", errors.New("drag: [x1, y1, x2, y2]")
	}
	x0, y0, x1, y1 := d[0], d[1], d[2], d[3]
	if err := mouseEvent(w, "mouseMoved", x0, y0, "none", 0, nil); err != nil {
		return "", err
	}
	if err := mouseEvent(w, "mousePressed", x0, y0, btn, 1, nil); err != nil {
		return "", err
	}
	const steps = 16
	for i := 1; i <= steps; i++ {
		if err := mouseEvent(w, "mouseMoved", x0+(x1-x0)*i/steps, y0+(y1-y0)*i/steps, btn, 0, map[string]any{"buttons": 1}); err != nil {
			return "", err
		}
		time.Sleep(12 * time.Millisecond)
	}
	if err := mouseEvent(w, "mouseReleased", x1, y1, btn, 1, nil); err != nil {
		return "", err
	}
	return fmt.Sprintf("dragged %d,%d → %d,%d", x0, y0, x1, y1), nil
}

// pageHold keeps keys down for secs (max 10), repeating like a real keyboard.
func pageHold(w *wsConn, keys string, secs float64) (string, error) {
	ks, err := parseCDPCombo(keys)
	if err != nil {
		return "", err
	}
	if secs <= 0 {
		secs = 1
	}
	secs = min(secs, 10)
	mods := comboMods(ks)
	for _, k := range ks {
		if err := sendCDPKey(w, k, "keyDown", mods, false); err != nil {
			return "", err
		}
	}
	end := time.Now().Add(time.Duration(secs * float64(time.Second)))
	var rerr error
	for time.Now().Before(end) && rerr == nil {
		time.Sleep(50 * time.Millisecond)
		for _, k := range ks {
			if cdpModBits[k.key] == 0 {
				rerr = sendCDPKey(w, k, "keyDown", mods, true)
			}
		}
	}
	if err := pageKeysUp(w, ks, mods); err != nil {
		return "", err
	}
	if rerr != nil {
		return "", rerr
	}
	return fmt.Sprintf("held %s %gs", keys, secs), nil
}

// pagePress presses a key or combo (Space, ArrowRight, ctrl+a).
func pagePress(w *wsConn, keys string) (string, error) {
	ks, err := parseCDPCombo(keys)
	if err != nil {
		return "", err
	}
	mods := comboMods(ks)
	for _, k := range ks {
		if err := sendCDPKey(w, k, "keyDown", mods, false); err != nil {
			return "", err
		}
	}
	time.Sleep(40 * time.Millisecond)
	if err := pageKeysUp(w, ks, mods); err != nil {
		return "", err
	}
	time.Sleep(80 * time.Millisecond)
	return "pressed " + keys, nil
}

// pageKeysUp lets go of every key, last pressed first; the first failure is returned after all were tried.
func pageKeysUp(w *wsConn, ks []cdpKey, mods int) error {
	var first error
	for i := len(ks) - 1; i >= 0; i-- {
		if err := sendCDPKey(w, ks[i], "keyUp", mods, false); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func pageClick(w *wsConn, x, y int, btn string, n int) error {
	if err := mouseEvent(w, "mouseMoved", x, y, "none", 0, nil); err != nil {
		return err
	}
	for c := 1; c <= n; c++ {
		if err := mouseEvent(w, "mousePressed", x, y, btn, c, nil); err != nil {
			return err
		}
		if err := mouseEvent(w, "mouseReleased", x, y, btn, c, nil); err != nil {
			return err
		}
	}
	time.Sleep(150 * time.Millisecond)
	return nil
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---------- console ----------

type consoleEntry struct {
	at    time.Time
	level string
	text  string
	where string
}

// consoleWatch stays attached to the page over its own DevTools connection, so messages
// logged between tool calls (and while the page loads) are kept.
var cw struct {
	mu       sync.Mutex
	target   string
	alive    bool
	since    time.Time // the page's last load, or when listening started
	navSeen  bool      // a load was seen while listening, so load-time errors are in
	entries  []consoleEntry
	dropped  int
	listened time.Time
}

func consoleAdd(e consoleEntry) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if len(cw.entries) >= 500 {
		cw.entries = cw.entries[1:]
		cw.dropped++
	}
	cw.entries = append(cw.entries, e)
}

// consoleWatchEnsure attaches the listener to the browser's tab when it isn't already.
func consoleWatchEnsure() {
	ts, err := cdpTargets()
	if err != nil {
		return
	}
	var t *cdpTarget
	for i := range ts {
		if ts[i].Type == "page" && ts[i].WS != "" {
			t = &ts[i]
			break
		}
	}
	if t == nil {
		return
	}
	cw.mu.Lock()
	if cw.alive && cw.target == t.ID {
		cw.mu.Unlock()
		return
	}
	cw.mu.Unlock()
	c, err := wsDial(t.WS)
	if err != nil {
		return
	}
	for i, m := range []string{"Runtime.enable", "Log.enable", "Page.enable"} {
		b, _ := json.Marshal(map[string]any{"id": 900000 + i, "method": m, "params": map[string]any{}})
		if err := c.send(b); err != nil {
			c.c.Close()
			return
		}
	}
	cw.mu.Lock()
	cw.target, cw.alive, cw.navSeen, cw.entries, cw.dropped = t.ID, true, false, nil, 0
	cw.since, cw.listened = time.Now(), time.Now()
	cw.mu.Unlock()
	go consoleWatchLoop(c, t.ID)
}

func consoleWatchLoop(c *wsConn, target string) {
	defer func() {
		c.c.Close()
		cw.mu.Lock()
		if cw.target == target {
			cw.alive = false
		}
		cw.mu.Unlock()
	}()
	if err := c.c.SetReadDeadline(time.Time{}); err != nil { // the listener waits as long as the tab lives
		return
	}
	for {
		m, err := c.recv()
		if err != nil {
			return
		}
		if e, nav, ok := parseConsoleEvent(m); nav {
			cw.mu.Lock()
			cw.entries, cw.dropped, cw.since, cw.navSeen = nil, 0, time.Now(), true
			cw.mu.Unlock()
		} else if ok {
			consoleAdd(e)
		}
	}
}

// parseConsoleEvent turns a DevTools event into a console line; nav is true when the tab loads a new page.
func parseConsoleEvent(m []byte) (e consoleEntry, nav, ok bool) {
	var ev struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(m, &ev) != nil || ev.Method == "" {
		return e, false, false
	}
	e.at = time.Now()
	type frame struct {
		URL  string `json:"url"`
		Line int    `json:"lineNumber"`
	}
	where := func(u string, line int) string {
		if u == "" {
			return ""
		}
		if i := strings.LastIndexAny(u, "/\\"); i >= 0 && i < len(u)-1 {
			u = u[i+1:]
		}
		return fmt.Sprintf("%s:%d", u, line+1)
	}
	switch ev.Method {
	case "Page.frameNavigated":
		var p struct {
			Frame struct {
				ParentID string `json:"parentId"`
			} `json:"frame"`
		}
		json.Unmarshal(ev.Params, &p)
		return e, p.Frame.ParentID == "", false
	case "Runtime.consoleAPICalled":
		var p struct {
			Type string `json:"type"`
			Args []struct {
				Value       any    `json:"value"`
				Description string `json:"description"`
			} `json:"args"`
			Stack struct {
				Frames []frame `json:"callFrames"`
			} `json:"stackTrace"`
		}
		json.Unmarshal(ev.Params, &p)
		var parts []string
		for _, a := range p.Args {
			switch v := a.Value.(type) {
			case nil:
				parts = append(parts, a.Description)
			case string:
				parts = append(parts, v)
			default:
				b, _ := json.Marshal(v)
				parts = append(parts, string(b))
			}
		}
		e.level, e.text = p.Type, strings.Join(parts, " ")
		if e.level == "warning" {
			e.level = "warn"
		}
		if len(p.Stack.Frames) > 0 {
			e.where = where(p.Stack.Frames[0].URL, p.Stack.Frames[0].Line)
		}
		return e, false, true
	case "Runtime.exceptionThrown":
		var p struct {
			D struct {
				Text      string `json:"text"`
				URL       string `json:"url"`
				Line      int    `json:"lineNumber"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		json.Unmarshal(ev.Params, &p)
		e.level, e.text = "uncaught", p.D.Text
		if d := p.D.Exception.Description; d != "" {
			e.text = strings.TrimSpace(strings.TrimPrefix(e.text, "Uncaught")) + " " + firstLine(d)
		}
		e.where = where(p.D.URL, p.D.Line)
		return e, false, true
	case "Log.entryAdded":
		var p struct {
			Entry struct {
				Level, Text, Source, URL string
				Line                     int `json:"lineNumber"`
			} `json:"entry"`
		}
		json.Unmarshal(ev.Params, &p)
		e.level, e.text = p.Entry.Level, p.Entry.Text
		if e.level == "warning" {
			e.level = "warn"
		}
		e.where = where(p.Entry.URL, p.Entry.Line)
		return e, false, true
	}
	return e, false, false
}

// consoleReport lists the page's console messages and uncaught errors since it was opened.
func consoleReport(pattern string, maxc int) string {
	cw.mu.Lock()
	entries := append([]consoleEntry(nil), cw.entries...)
	since, navSeen, alive, dropped, listened := cw.since, cw.navSeen, cw.alive, cw.dropped, cw.listened
	cw.mu.Unlock()
	if !alive && listened.IsZero() {
		return "console: not listening yet; open the page with the browser tool first"
	}
	var re *regexp.Regexp
	if pattern != "" {
		var err error
		if re, err = regexp.Compile("(?i)" + pattern); err != nil {
			re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pattern))
		}
	}
	var b strings.Builder
	errs, shown := 0, 0
	for _, e := range entries {
		if e.level == "error" || e.level == "uncaught" {
			errs++
		}
		line := fmt.Sprintf("%s [%s] %s", e.at.Format("15:04:05"), e.level, strings.TrimSpace(e.text))
		if e.where != "" {
			line += " (" + e.where + ")"
		}
		if re != nil && !re.MatchString(line) {
			continue
		}
		shown++
		b.WriteString(clip(line, 600) + "\n")
	}
	head := fmt.Sprintf("console since %s: %d message(s), %d error(s)", since.Format("15:04:05"), len(entries), errs)
	if re != nil {
		head += fmt.Sprintf(", %d matching %q", shown, pattern)
	}
	if dropped > 0 {
		head += fmt.Sprintf(" (%d older dropped)", dropped)
	}
	if !navSeen {
		head += "\n(listening started after the page loaded: open it again to catch load-time errors)"
	}
	if !alive {
		head += "\n(the listener stopped: the tab closed or the browser restarted)"
	}
	if shown == 0 {
		return head
	}
	body, note := capText(strings.TrimRight(b.String(), "\n"), maxc, "browser console")
	return head + "\n" + body + note
}
