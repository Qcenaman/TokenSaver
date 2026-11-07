//go:build !windows && !linux

package main

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func recycleBatch(paths []string) error { return errors.New("no Recycle Bin on this system") }
func useRecycleBin(p string) bool       { return false }
func holdRoot(p string) string          { return filepath.Join(appHome, "trash") }
func stripVol(p string) string          { return strings.TrimPrefix(p, "/") }

func desktopDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Desktop")
}

func setFileTimes(p string, st fs.FileInfo)   { os.Chtimes(p, st.ModTime(), st.ModTime()) }
func setFileAttrs(p string, st fs.FileInfo)   {}
func creationTime(info fs.FileInfo) time.Time { return info.ModTime() }
func isCrossDevice(err error) bool            { return errors.Is(err, syscall.EXDEV) }
func sameVolume(a, b string) bool             { return true }

func restoreRecycled(orig string, at time.Time) error {
	return errors.New("no Recycle Bin on this system")
}

func listenerPid(port int) int { return 0 }

func quietCmd(name string, args ...string) *exec.Cmd { return exec.Command(name, args...) }
