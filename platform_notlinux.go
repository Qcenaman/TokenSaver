//go:build !linux

package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Counterparts of the Linux-only helpers, so shared code builds everywhere.
// Shared code only reaches them behind !isWin checks or where they fit any OS.

var errLinuxOnly = errors.New("only available on Linux")

func installHint(tool string) string {
	if !isWin {
		return "install " + tool
	}
	id := map[string]string{"ffmpeg": "ffmpeg", "magick": "ImageMagick.ImageMagick", "pdftoppm": "oschwartz10612.Poppler",
		"soffice": "TheDocumentFoundation.LibreOffice", "tesseract": "UB-Mannheim.TesseractOCR"}[tool]
	if id == "" {
		id = tool
	}
	return "winget install " + id
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func openInEditor(p string) error {
	if isWin {
		return exec.Command("notepad.exe", p).Start()
	}
	return exec.Command("open", "-t", p).Start()
}

func ocrAvailable() bool         { return false }
func otherGPUInfo() string       { return "" }
func consoleSetup()              { consoleInstall() }
func linuxInstallExtras(string)  {}
func linuxDisconnectClaudeCode() {}

func linuxClaudeConfigPaths() []string         { return nil }
func linuxConnectClaudeCode(target string) int { return 0 }
func firewallAllowLinux(port int) string {
	return "firewall: open TCP port inbound yourself"
}
func writeLinuxShortcut(name string) (string, error) { return "", nil }
func sofficeConvert(src, dst string) error           { return errLinuxOnly }

func linuxWhisperCLI(ctx context.Context, dir string) (string, error) { return "", errLinuxOnly }
func runLinuxOCR(ctx context.Context, r ocrReq, each func(ocrPage)) ([]byte, error) {
	return nil, errors.New("OCR needs Windows or Linux")
}
func atspiTextLines(w winInfo) ([]string, error)                { return nil, errLinuxOnly }
func atspiDo(w winInfo, op, name, value string) (string, error) { return "", errLinuxOnly }
