//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// "tokensaver setup": everything a fresh Ubuntu needs, one question per system change.

type setupPkg struct{ pkg, why string }

var setupPkgs = []setupPkg{
	{"tesseract-ocr", "OCR of scans, pictures and screens"},
	{"poppler-utils", "PDF pages and page counts"},
	{"imagemagick", "screen/window captures and picture formats"},
	{"ffmpeg", "media: audio/video to text"},
	{"xdotool", "mouse and keys (X11)"},
	{"wmctrl", "window list and focus (X11)"},
	{"xclip", "clipboard (X11)"},
	{"wl-clipboard", "clipboard (Wayland)"},
	{"python3-gi", "window text through accessibility"},
	{"gir1.2-atspi-2.0", "window text through accessibility"},
	{"7zip", "archives"},
	{"adb", "phone tool"},
	{"git", "git tool"},
}

// optionalPkgs are large; asked about separately.
var optionalPkgs = []setupPkg{
	{"libreoffice-core libreoffice-writer libreoffice-calc libreoffice-impress", "office convert (about 300 MB)"},
}

func pkgInstalled(p string) bool {
	out, err := exec.Command("dpkg-query", "-W", "-f=${Status}", p).Output()
	return err == nil && strings.Contains(string(out), "install ok installed")
}

func missingPkgs(list []setupPkg) (names []string, why []string) {
	seen := map[string]bool{}
	for _, p := range list {
		for _, n := range strings.Fields(p.pkg) {
			if !pkgInstalled(n) {
				names = append(names, n)
				if !seen[p.why] {
					why = append(why, p.why)
					seen[p.why] = true
				}
			}
		}
	}
	return
}

func aptInstall(pkgs []string) error {
	args := append([]string{"apt-get", "install", "-y"}, pkgs...)
	var c *exec.Cmd
	switch {
	case os.Geteuid() == 0:
		c = exec.Command(args[0], args[1:]...)
	case helperUp():
		ep, err := startElevated(elevRequest{Command: strings.Join(args, " "), Env: []string{"DEBIAN_FRONTEND=noninteractive"}}, os.Stdout)
		if err != nil {
			return err
		}
		<-ep.done
		if ep.err != nil {
			return ep.err
		}
		if ep.code != nil && *ep.code != 0 {
			return fmt.Errorf("apt-get exited with %d", *ep.code)
		}
		return nil
	default:
		c = exec.Command("sudo", args...)
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c.Run()
}

func consoleSetup() {
	fmt.Printf("TokenSaver %s setup for %s\n\n", appVersion, osRelease())
	if _, err := exec.LookPath("apt-get"); err == nil {
		if names, why := missingPkgs(setupPkgs); len(names) > 0 {
			fmt.Println("Missing Ubuntu packages:", strings.Join(names, " "))
			fmt.Println("  for:", strings.Join(why, "; "))
			if askYes("Install them now (needs your password)? [Y/n] ", true) {
				if err := aptInstall(names); err != nil {
					fmt.Println("Not all packages installed:", err)
				}
			}
		} else {
			fmt.Println("Ubuntu packages: all there.")
		}
		if names, why := missingPkgs(optionalPkgs); len(names) > 0 && sofficeBin() == "" {
			fmt.Println("\nOptional:", strings.Join(why, "; "))
			if askYes("Install LibreOffice too? [y/N] ", false) {
				if err := aptInstall(names); err != nil {
					fmt.Println("LibreOffice not installed:", err)
				}
			}
		}
	}
	fmt.Println()
	consoleInstall()
	if !exists(helperUnit) {
		fmt.Println()
		if askYes("Install the admin helper, so Claude can run admin commands without a password each time (destructive ones still ask you)? [Y/n] ", true) {
			consoleInstallHelper()
		}
	}
	fmt.Println()
	if askYes("Run tokensaver serve as a service (port 8766, for RemoteAI and the phone link)? [Y/n] ", true) {
		consoleInstallLink()
	}
	if onWayland() {
		fmt.Println("\nThis is a Wayland session: window text and click-by-name work, but moving the mouse,")
		fmt.Println("switching windows and screenshots are limited by the desktop. For full control pick")
		fmt.Println("\"Ubuntu on Xorg\" with the gear button on the login screen.")
	}
}
