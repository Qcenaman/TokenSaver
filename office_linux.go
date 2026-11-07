//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func sofficeBin() string {
	for _, n := range []string{"soffice", "libreoffice"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	for _, p := range []string{"/usr/lib/libreoffice/program/soffice", "/opt/libreoffice/program/soffice", "/snap/bin/libreoffice"} {
		if exists(p) {
			return p
		}
	}
	return ""
}

// sofficeConvert converts with LibreOffice (headless, its own profile so an open
// LibreOffice window doesn't block it), the Linux stand-in for Word/Excel/PowerPoint.
func sofficeConvert(src, dst string) error {
	bin := sofficeBin()
	if bin == "" {
		return errors.New("convert needs LibreOffice (" + installHint("soffice") + ")")
	}
	se := strings.ToLower(filepath.Ext(src))
	if officeSrcApp[se] == "" {
		return fmt.Errorf("can't convert %s files", se)
	}
	de := strings.TrimPrefix(strings.ToLower(filepath.Ext(dst)), ".")
	if de == "" {
		return errors.New("to needs an extension like .pdf")
	}
	out, err := os.MkdirTemp(filepath.Join(appHome), "convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(out)
	prof := filepath.Join(appHome, "libreoffice-profile")
	os.MkdirAll(prof, 0o755)
	args := []string{"-env:UserInstallation=" + (&url.URL{Scheme: "file", Path: prof}).String(), "--headless", "--norestore"}
	if se == ".pdf" {
		args = append(args, "--infilter=writer_pdf_import")
	}
	args = append(args, "--convert-to", de, "--outdir", out, src)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	b, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	if ctx.Err() != nil {
		return errors.New("LibreOffice took longer than 5 minutes")
	}
	got := filepath.Join(out, strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))+"."+de)
	if !exists(got) {
		msg := strings.TrimSpace(string(b))
		if err != nil {
			msg = err.Error() + " " + msg
		}
		return errors.New("LibreOffice couldn't convert it: " + clip(msg, 300))
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	os.Remove(dst)
	if err := os.Rename(got, dst); err != nil {
		if !isCrossDevice(err) {
			return err
		}
		if err := copyPlain(got, dst); err != nil {
			return err
		}
	}
	return nil
}
