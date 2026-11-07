//go:build linux

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Deleted files go to the desktop's Trash (freedesktop.org spec), so they can be
// restored from Files → Trash just like the Windows Recycle Bin.

func homeTrash() string {
	d := os.Getenv("XDG_DATA_HOME")
	if d == "" {
		h, _ := os.UserHomeDir()
		d = filepath.Join(h, ".local", "share")
	}
	return filepath.Join(d, "Trash")
}

func devOf(p string) (uint64, bool) {
	var st syscall.Stat_t
	if syscall.Lstat(p, &st) != nil {
		return 0, false
	}
	return uint64(st.Dev), true
}

// mountTop: the mount point that holds p.
func mountTop(p string) string {
	dev, ok := devOf(p)
	if !ok {
		return ""
	}
	cur := p
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return cur
		}
		if d, ok := devOf(parent); !ok || d != dev {
			return cur
		}
		cur = parent
	}
}

// trashFor picks the trash folder for p: the home trash on the same disk, else
// <mount>/.Trash-<uid>. "" means p can't be trashed (it then goes to the holding folder).
func trashFor(p string) string {
	dev, ok := devOf(p)
	if !ok {
		return ""
	}
	ht := homeTrash()
	os.MkdirAll(filepath.Join(ht, "files"), 0o700)
	os.MkdirAll(filepath.Join(ht, "info"), 0o700)
	if d, ok := devOf(ht); ok && d == dev {
		return ht
	}
	top := mountTop(p)
	if top == "" || top == "/" {
		return ""
	}
	t := filepath.Join(top, fmt.Sprintf(".Trash-%d", os.Getuid()))
	if os.MkdirAll(filepath.Join(t, "files"), 0o700) != nil || os.MkdirAll(filepath.Join(t, "info"), 0o700) != nil {
		return ""
	}
	return t
}

func useRecycleBin(p string) bool { return trashFor(p) != "" }

// trashPathField is the Path= value: absolute for the home trash, relative to the mount otherwise.
func trashPathField(trash, p string) string {
	if trash != homeTrash() {
		if rel, err := filepath.Rel(filepath.Dir(trash), p); err == nil {
			p = rel
		}
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func trashOne(p string) error {
	p, _ = filepath.Abs(p)
	trash := trashFor(p)
	if trash == "" {
		return errors.New("no Trash on this disk")
	}
	base := filepath.Base(p)
	name := base
	var info *os.File
	var err error
	for i := 1; i < 10000; i++ {
		info, err = os.OpenFile(filepath.Join(trash, "info", name+".trashinfo"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return err
		}
		ext := filepath.Ext(base)
		name = fmt.Sprintf("%s.%d%s", strings.TrimSuffix(base, ext), i+1, ext)
	}
	if info == nil {
		return err
	}
	fmt.Fprintf(info, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", trashPathField(trash, p), time.Now().Format("2006-01-02T15:04:05"))
	info.Close()
	if err := os.Rename(p, filepath.Join(trash, "files", name)); err != nil {
		os.Remove(filepath.Join(trash, "info", name+".trashinfo"))
		return err
	}
	return nil
}

func recycleBatch(paths []string) error {
	var errs []string
	for _, p := range paths {
		if err := trashOne(p); err != nil {
			errs = append(errs, filepath.Base(p)+": "+err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func holdRoot(p string) string { return filepath.Join(appHome, "trash") }
func stripVol(p string) string { return strings.TrimPrefix(p, "/") }

// desktopDir follows the user's language (xdg-user-dirs), e.g. ~/Bureaublad.
func desktopDir() string {
	h, _ := os.UserHomeDir()
	if b, err := os.ReadFile(filepath.Join(h, ".config", "user-dirs.dirs")); err == nil {
		sc := bufio.NewScanner(strings.NewReader(string(b)))
		for sc.Scan() {
			if v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "XDG_DESKTOP_DIR="); ok {
				return strings.ReplaceAll(strings.Trim(v, `"`), "$HOME", h)
			}
		}
	}
	return filepath.Join(h, "Desktop")
}

func setFileTimes(p string, st fs.FileInfo) { os.Chtimes(p, st.ModTime(), st.ModTime()) }

// setFileAttrs carries the permission bits over (Windows copies its file attributes here).
func setFileAttrs(p string, st fs.FileInfo) { os.Chmod(p, st.Mode().Perm()) }

// creationTime: FileInfo has no birth time on Linux, so the modified time stands in.
func creationTime(info fs.FileInfo) time.Time { return info.ModTime() }

func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

func sameVolume(a, b string) bool {
	for !exists(a) && filepath.Dir(a) != a {
		a = filepath.Dir(a)
	}
	for !exists(b) && filepath.Dir(b) != b {
		b = filepath.Dir(b)
	}
	da, ok1 := devOf(a)
	db, ok2 := devOf(b)
	return !ok1 || !ok2 || da == db
}

// restoreRecycled puts back the trashed copy of orig deleted closest to at.
func restoreRecycled(orig string, at time.Time) error {
	orig, _ = filepath.Abs(orig)
	if exists(orig) {
		return fmt.Errorf("%s exists again; restore it from Trash by hand", orig)
	}
	trashes := []string{homeTrash()}
	if top := mountTop(filepath.Dir(orig)); top != "" && top != "/" {
		trashes = append(trashes, filepath.Join(top, fmt.Sprintf(".Trash-%d", os.Getuid())))
	}
	best, bestInfo := "", ""
	bestGap := time.Duration(1<<62 - 1)
	for _, t := range trashes {
		infos, _ := filepath.Glob(filepath.Join(t, "info", "*.trashinfo"))
		for _, ip := range infos {
			path, when := readTrashInfo(t, ip)
			if path != orig {
				continue
			}
			gap := when.Sub(at)
			if gap < 0 {
				gap = -gap
			}
			if gap < bestGap {
				bestGap = gap
				bestInfo = ip
				best = filepath.Join(t, "files", strings.TrimSuffix(filepath.Base(ip), ".trashinfo"))
			}
		}
	}
	if best == "" {
		return errors.New("not found in Trash (emptied?)")
	}
	os.MkdirAll(filepath.Dir(orig), 0o755)
	if err := os.Rename(best, orig); err != nil {
		return err
	}
	os.Remove(bestInfo)
	return nil
}

func readTrashInfo(trash, ip string) (string, time.Time) {
	b, err := os.ReadFile(ip)
	if err != nil {
		return "", time.Time{}
	}
	var path string
	var when time.Time
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "Path="); ok {
			if u, err := url.PathUnescape(v); err == nil {
				path = u
			} else {
				path = v
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(filepath.Dir(trash), path)
			}
		}
		if v, ok := strings.CutPrefix(l, "DeletionDate="); ok {
			when, _ = time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(v), time.Local)
		}
	}
	return path, when
}

func quietCmd(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
