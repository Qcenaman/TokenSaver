//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// On Linux the Windows OCR script is replaced by tesseract (OCR), pdftoppm (PDF pages)
// and ImageMagick or ffmpeg (other picture formats). Same requests, same results.

func ocrAvailable() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}

var ocrLangsOnce sync.Once
var ocrLangs string

// tesseractLangs: TOKENSAVER_OCR_LANG, else English plus up to two other installed languages.
func tesseractLangs() string {
	ocrLangsOnce.Do(func() {
		if v := os.Getenv("TOKENSAVER_OCR_LANG"); v != "" {
			ocrLangs = v
			return
		}
		out, _ := exec.Command("tesseract", "--list-langs").CombinedOutput()
		langs := []string{}
		hasEng := false
		for _, l := range strings.Split(string(out), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.Contains(l, " ") || l == "osd" || l == "equ" {
				continue
			}
			if l == "eng" {
				hasEng = true
				continue
			}
			if len(langs) < 2 {
				langs = append(langs, l)
			}
		}
		if hasEng || len(langs) == 0 {
			langs = append([]string{"eng"}, langs...)
		}
		ocrLangs = strings.Join(langs, "+")
	})
	return ocrLangs
}

func ocrTmpDir() (string, error) {
	d := filepath.Join(appHome, "tmp")
	os.MkdirAll(d, 0o755)
	return os.MkdirTemp(d, "ocr-")
}

// tesseractUpscaled OCRs small pictures (screens, UI) at twice the size, as Windows OCR does,
// and maps sizes and word boxes back to the original pixels.
func tesseractUpscaled(ctx context.Context, img string, n int, boxes, upscale bool, dir string) (ocrPage, error) {
	w, h := imageSize(img)
	if !upscale || w == 0 || w >= 1600 || h >= 1600 || !hasCmd("convert") {
		return tesseractPage(ctx, img, n, boxes)
	}
	big := filepath.Join(dir, "up.png")
	if exec.CommandContext(ctx, "convert", img, "-resize", "200%", big).Run() != nil {
		return tesseractPage(ctx, img, n, boxes)
	}
	pg, err := tesseractPage(ctx, big, n, boxes)
	os.Remove(big)
	pg.W, pg.H = w, h
	for i := range pg.Lines {
		for j := range pg.Lines[i].Words {
			wd := &pg.Lines[i].Words[j]
			wd.X, wd.Y, wd.W, wd.H = wd.X/2, wd.Y/2, (wd.W+1)/2, (wd.H+1)/2
		}
	}
	return pg, err
}

// tesseractPage OCRs one image file into a page (word boxes in image pixels).
func tesseractPage(ctx context.Context, img string, n int, boxes bool) (ocrPage, error) {
	pg := ocrPage{N: n}
	cmd := exec.CommandContext(ctx, "tesseract", img, "stdout", "-l", tesseractLangs(), "--psm", "3", "tsv")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return pg, errors.New("OCR took too long")
		}
		return pg, fmt.Errorf("tesseract: %v %s", err, clip(strings.TrimSpace(stderr.String()), 300))
	}
	type key struct{ b, p, l int }
	lines := map[key]*ocrLine{}
	var order []key
	for i, row := range strings.Split(string(out), "\n") {
		f := strings.Split(row, "\t")
		if i == 0 || len(f) < 12 {
			continue
		}
		level, _ := strconv.Atoi(f[0])
		x, _ := strconv.Atoi(f[6])
		y, _ := strconv.Atoi(f[7])
		w, _ := strconv.Atoi(f[8])
		h, _ := strconv.Atoi(f[9])
		if level == 1 {
			pg.W, pg.H = w, h
			continue
		}
		text := strings.TrimSpace(f[11])
		if level != 5 || text == "" {
			continue
		}
		b, _ := strconv.Atoi(f[2])
		p, _ := strconv.Atoi(f[3])
		l, _ := strconv.Atoi(f[4])
		k := key{b, p, l}
		ln := lines[k]
		if ln == nil {
			ln = &ocrLine{}
			lines[k] = ln
			order = append(order, k)
		}
		if ln.Text != "" {
			ln.Text += " "
		}
		ln.Text += text
		if boxes {
			ln.Words = append(ln.Words, ocrWord{X: x, Y: y, W: w, H: h, Text: text})
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if a.b != b.b {
			return a.b < b.b
		}
		if a.p != b.p {
			return a.p < b.p
		}
		return a.l < b.l
	})
	for _, k := range order {
		pg.Lines = append(pg.Lines, *lines[k])
	}
	return pg, nil
}

// pdfPageCount asks pdfinfo; 0 when unknown.
func pdfPageCount(ctx context.Context, p string) int {
	out, err := exec.CommandContext(ctx, "pdfinfo", p).Output()
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(l, "Pages:"); ok {
			n, _ := strconv.Atoi(strings.TrimSpace(v))
			return n
		}
	}
	return 0
}

// renderPDFPage writes page n as an image; scaleTo limits the longest side (0 = 200 dpi).
func renderPDFPage(ctx context.Context, pdf string, n, scaleTo int, jpeg bool, dir string) (string, error) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		return "", errors.New("PDF pages need pdftoppm (" + installHint("pdftoppm") + ")")
	}
	base := filepath.Join(dir, fmt.Sprintf("p%d", n))
	args := []string{"-f", strconv.Itoa(n), "-l", strconv.Itoa(n), "-singlefile"}
	if jpeg {
		args = append(args, "-jpeg", "-jpegopt", "quality=85")
	} else {
		args = append(args, "-png")
	}
	if scaleTo > 0 {
		args = append(args, "-scale-to", strconv.Itoa(scaleTo))
	} else {
		args = append(args, "-r", "200")
	}
	out, err := exec.CommandContext(ctx, "pdftoppm", append(args, pdf, base)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("pdftoppm: %v %s", err, clip(strings.TrimSpace(string(out)), 200))
	}
	for _, ext := range []string{".jpg", ".png"} {
		if exists(base + ext) {
			return base + ext, nil
		}
	}
	return "", fmt.Errorf("the PDF has no page %d", n)
}

// convertToJPEG turns any picture ImageMagick, heif-convert or ffmpeg can read into a JPEG.
func convertToJPEG(ctx context.Context, src string, maxPx int, dir string) ([]byte, error) {
	dst := filepath.Join(dir, "out.jpg")
	try := func(name string, args ...string) bool {
		if _, err := exec.LookPath(name); err != nil {
			return false
		}
		os.Remove(dst)
		return exec.CommandContext(ctx, name, args...).Run() == nil && exists(dst)
	}
	resize := []string{}
	vf := "null"
	if maxPx > 0 {
		resize = []string{"-resize", fmt.Sprintf("%dx%d>", maxPx, maxPx)}
		vf = fmt.Sprintf("scale='min(%d,iw)':'min(%d,ih)':force_original_aspect_ratio=decrease", maxPx, maxPx)
	}
	ok := try("magick", append(append([]string{src + "[0]", "-auto-orient"}, resize...), "-quality", "85", dst)...) ||
		try("convert", append(append([]string{src + "[0]", "-auto-orient"}, resize...), "-quality", "85", dst)...)
	if !ok {
		low := strings.ToLower(src)
		if strings.HasSuffix(low, ".heic") || strings.HasSuffix(low, ".heif") || strings.HasSuffix(low, ".avif") {
			if try("heif-convert", "-q", "85", src, dst) && maxPx > 0 {
				try("ffmpeg", "-y", "-v", "error", "-i", dst, "-vf", vf, dst+".jpg")
				if exists(dst + ".jpg") {
					os.Rename(dst+".jpg", dst)
				}
			}
			ok = exists(dst)
		}
	}
	if !ok {
		ok = try("ffmpeg", "-y", "-v", "error", "-i", src, "-frames:v", "1", "-vf", vf, "-q:v", "3", dst)
	}
	if !ok {
		return nil, fmt.Errorf("can't open %s images (%s)", filepath.Ext(src), installHint("magick"))
	}
	return os.ReadFile(dst)
}

func imageSize(p string) (int, int) {
	f, err := os.Open(p)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	c, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return c.Width, c.Height
}

// runLinuxOCR does what the Windows OCR script does, one request at a time.
func runLinuxOCR(ctx context.Context, r ocrReq, each func(ocrPage)) ([]byte, error) {
	dir, err := ocrTmpDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	emit := func(p ocrPage) {
		if each != nil {
			each(p)
		}
	}
	needTess := func() error {
		if !ocrAvailable() {
			return errors.New("reading text in images needs tesseract (" + installHint("tesseract") + ")")
		}
		return nil
	}
	switch r.mode {
	case "convert":
		jpg, err := convertToJPEG(ctx, r.path, r.maxPx, dir)
		if err != nil {
			return nil, err
		}
		w, h := imageSize(filepath.Join(dir, "out.jpg"))
		emit(ocrPage{W: w, H: h})
		return jpg, nil
	case "pdfimage":
		if len(r.pages) == 0 {
			return nil, errors.New("no page given")
		}
		img, err := renderPDFPage(ctx, r.path, r.pages[0], r.maxPx, true, dir)
		if err != nil {
			return nil, err
		}
		w, h := imageSize(img)
		emit(ocrPage{N: r.pages[0], W: w, H: h})
		return os.ReadFile(img)
	case "image", "stdin":
		if err := needTess(); err != nil {
			return nil, err
		}
		src := r.path
		if r.mode == "stdin" {
			src = filepath.Join(dir, "in.png")
			if err := os.WriteFile(src, r.stdin, 0o644); err != nil {
				return nil, err
			}
		} else if ext := strings.ToLower(filepath.Ext(src)); ext == ".heic" || ext == ".heif" || ext == ".avif" || ext == ".webp" {
			if jpg, err := convertToJPEG(ctx, src, 0, dir); err == nil {
				src = filepath.Join(dir, "in.jpg")
				os.WriteFile(src, jpg, 0o644)
			}
		}
		pg, err := tesseractUpscaled(ctx, src, 0, r.boxes, r.upscale, dir)
		if err != nil {
			return nil, err
		}
		emit(pg)
		return nil, nil
	case "pdf":
		if err := needTess(); err != nil {
			return nil, err
		}
		pages := r.pages
		if len(pages) == 0 {
			for i := 1; i <= pdfPageCount(ctx, r.path); i++ {
				pages = append(pages, i)
			}
		}
		for _, n := range pages {
			img, err := renderPDFPage(ctx, r.path, n, 0, false, dir)
			if err != nil {
				return nil, err
			}
			pg, err := tesseractPage(ctx, img, n, r.boxes)
			os.Remove(img)
			if err != nil {
				return nil, err
			}
			emit(pg)
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown OCR mode %q", r.mode)
}
