//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

// installHint is the apt command for a missing tool on Ubuntu.
func installHint(tool string) string {
	pkg := map[string]string{
		"ffmpeg": "ffmpeg", "tesseract": "tesseract-ocr", "pdftoppm": "poppler-utils", "soffice": "libreoffice",
		"7z": "7zip", "adb": "adb", "xdotool": "xdotool", "wmctrl": "wmctrl", "xclip": "xclip",
		"wl-copy": "wl-clipboard", "ydotool": "ydotool", "magick": "imagemagick", "convert": "imagemagick",
		"cmake": "cmake build-essential", "git": "git", "heif-convert": "libheif-examples",
		"import": "imagemagick", "xsel": "xsel", "wl-paste": "wl-clipboard", "grim": "grim",
	}[tool]
	if pkg == "" {
		pkg = tool
	}
	return "sudo apt install -y " + pkg
}

// openInEditor opens a file in the desktop's default editor ($VISUAL, $EDITOR in a terminal, else xdg-open).
func openInEditor(p string) error {
	if _, err := exec.LookPath("xdg-open"); err == nil && os.Getenv("DISPLAY")+os.Getenv("WAYLAND_DISPLAY") != "" {
		return exec.Command("xdg-open", p).Start()
	}
	for _, v := range []string{"VISUAL", "EDITOR"} {
		if e := os.Getenv(v); e != "" {
			c := exec.Command(e, p)
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			return c.Run()
		}
	}
	return errors.New("no editor found")
}

// linuxWhisperCLI finds whisper-cli (PATH or earlier build) or builds whisper.cpp from source:
// whisper.cpp publishes no Linux binaries. Vulkan (AMD/Intel/NVIDIA) is used when its SDK is there.
func linuxWhisperCLI(ctx context.Context, dir string) (string, error) {
	built := filepath.Join(dir, "src", "build", "bin", "whisper-cli")
	if exists(built) {
		return built, nil
	}
	if p, err := exec.LookPath("whisper-cli"); err == nil {
		return p, nil
	}
	for _, t := range []string{"git", "cmake", "c++"} {
		if _, err := exec.LookPath(t); err != nil {
			return "", errors.New("whisper.cpp has to be built on Linux and " + t + " is missing (sudo apt install -y git cmake build-essential; add libvulkan-dev glslc for the GPU)")
		}
	}
	src := filepath.Join(dir, "src")
	if !exists(filepath.Join(src, "CMakeLists.txt")) {
		os.RemoveAll(src) // only a half-finished clone of our own can be here
		if out, err := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "https://github.com/ggml-org/whisper.cpp", src).CombinedOutput(); err != nil {
			return "", errors.New("git clone whisper.cpp: " + clip(string(out), 300))
		}
	}
	args := []string{"-S", src, "-B", filepath.Join(src, "build"), "-DCMAKE_BUILD_TYPE=Release", "-DWHISPER_BUILD_TESTS=OFF"}
	if _, err := exec.LookPath("glslc"); err == nil {
		args = append(args, "-DGGML_VULKAN=ON")
	}
	if out, err := exec.CommandContext(ctx, "cmake", args...).CombinedOutput(); err != nil {
		return "", errors.New("cmake: " + clip(string(out), 400))
	}
	jobs := strconv.Itoa(max(1, runtime.NumCPU()-1))
	if out, err := exec.CommandContext(ctx, "cmake", "--build", filepath.Join(src, "build"), "--target", "whisper-cli", "-j", jobs).CombinedOutput(); err != nil {
		return "", errors.New("building whisper.cpp: " + clip(string(out), 400))
	}
	if !exists(built) {
		return "", errors.New("whisper.cpp built but whisper-cli wasn't produced")
	}
	return built, nil
}
