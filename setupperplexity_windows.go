//go:build windows

package main

// The setup installer for other people (TokenSaver-Setup.exe, or `tokensaver setup-perplexity`):
// installs TokenSaver for this user, signs in to Tailscale, installs the boot service (one
// Windows permission prompt), turns on the permanent Funnel address and adds the connector in
// Perplexity, so Perplexity can code on this PC with auto-yes. Every step says what it does and
// what to do when it fails; a step already done is skipped, so running it again is safe.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const connectorName = "TokenSaver"

func setupPerplexityMain(args []string) int {
	if len(args) > 0 && args[0] == "--test-connector-form" { // fills the form, then Cancel
		if err := perplexityAddConnector("https://example.invalid/test", true); err != nil {
			fmt.Println("form test failed:", err)
			return 1
		}
		fmt.Println("form test passed: the form was filled and cancelled")
		return 0
	}
	fmt.Println("TokenSaver setup for Perplexity", appVersion)
	fmt.Println("Lets Perplexity code on this PC and answers its \"yes?\" questions for you.")
	fmt.Println()
	fail := func(step, why, fix string) int {
		fmt.Printf("\n%s FAILED: %s\n%s\nRun this setup again when that is fixed; finished steps are skipped.\n", step, why, fix)
		waitEnter("Press Enter to close.", 0)
		return 1
	}

	fmt.Println("1/5 Installing TokenSaver...")
	if err := setupCopyProgram(); err != nil {
		return fail("Install", err.Error(), "Close any running TokenSaver and try again.")
	}
	fmt.Println("    installed in", appRoot())

	fmt.Println("2/5 Tailscale (gives this PC a permanent web address for Perplexity)...")
	if err := setupTailscale(); err != nil {
		return fail("Tailscale", err.Error(), "Install it from https://tailscale.com/download/windows and sign in.")
	}
	fmt.Println("    signed in")

	fmt.Println("3/5 Installing the TokenSaver service (Windows asks for permission once)...")
	if err := setupService(); err != nil {
		return fail("Service", err.Error(), "Answer Yes on the Windows permission prompt.")
	}
	fmt.Println("    running")

	fmt.Println("4/5 Turning on the permanent address (Tailscale Funnel)...")
	url, err := setupFunnel()
	if err != nil {
		return fail("Address", err.Error(), "In the Tailscale page that opened, allow Funnel, then run setup again.")
	}
	setClipText(url)
	fmt.Println("    ready (also copied to the clipboard)")

	fmt.Println("5/5 Adding the connector in Perplexity...")
	if err := perplexityAddConnector(url, false); err != nil {
		fmt.Println("    could not add it by itself:", err)
		fmt.Println()
		fmt.Println("    Add it by hand (the address is on the clipboard):")
		fmt.Println("      1. In Perplexity open Plugins > Add custom > Add MCP connector")
		fmt.Println("      2. Name: " + connectorName + "    MCP server URL: paste (Ctrl+V)")
		fmt.Println("      3. Tick \"I understand...\" and press Add")
		fmt.Println("    Address:", url)
	} else {
		fmt.Println("    added")
	}
	howTo := setupWriteHowTo(url)
	fmt.Println()
	fmt.Println("All set. How to use it:")
	fmt.Println("  - Open a NEW Perplexity chat with the TokenSaver connector on and write, for example:")
	fmt.Println("      Use TokenSaver: turn on project mode all. Then make me a to-do app in C:\\Projects\\todo")
	fmt.Println("  - Keep the Perplexity app open while it works; TokenSaver answers its yes questions.")
	fmt.Println("  - Stop with: Use TokenSaver: turn off project mode  (it also stops by itself after 8 hours)")
	fmt.Println("  - After a restart, sign in to Windows; Perplexity can use this PC again from then on.")
	fmt.Println("  - Keep the connector address private: whoever has it can use this PC.")
	if howTo != "" {
		fmt.Println("  These notes are also on your Desktop:", filepath.Base(howTo))
	}
	waitEnter("Press Enter to close.", 0)
	return 0
}

// setupCopyProgram copies this program to <root>\app\tokensaver.exe, marks the root (so the
// service finds the same folder) and puts it on the user's PATH.
func setupCopyProgram() error {
	target := installPath()
	root := filepath.Dir(filepath.Dir(target))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, rootMarker), []byte("TokenSaver keeps everything in this folder.\n"), 0o644); err != nil {
		return err
	}
	exe, _ := os.Executable()
	if !samePath(exe, target) {
		if exists(target) {
			old := target + ".old"
			os.Remove(old)
			if err := os.Rename(target, old); err != nil {
				return fmt.Errorf("the installed copy is in use: %v", err)
			}
		}
		if err := copyPlain(exe, target); err != nil {
			return err
		}
	}
	os.WriteFile(filepath.Join(filepath.Dir(target), "version.txt"), []byte(appVersion+"\n"), 0o644)
	addToUserPath(filepath.Dir(target))
	return nil
}

var tsURLRe = regexp.MustCompile(`https://login\.tailscale\.com/\S+`)

func tsBackendState(ts string) string {
	out, _ := runQuiet(10*time.Second, ts, "status", "--json", "--peers=false")
	var st struct{ BackendState string }
	json.Unmarshal([]byte(out), &st)
	return st.BackendState
}

// setupTailscale installs Tailscale when missing (winget) and signs in through the browser.
func setupTailscale() error {
	if tailscaleExe() == "" {
		if _, err := exec.LookPath("winget"); err != nil {
			openURL("https://tailscale.com/download/windows")
			fmt.Println("    Install Tailscale from the page that opened, then come back here.")
		} else {
			fmt.Println("    installing Tailscale (Windows may ask for permission)...")
			c := exec.Command("winget", "install", "--id", "Tailscale.Tailscale", "-e", "--silent",
				"--accept-package-agreements", "--accept-source-agreements")
			c.Stdout, c.Stderr = os.Stdout, os.Stderr
			c.Run()
		}
		for i := 0; i < 200 && tailscaleExe() == ""; i++ {
			time.Sleep(3 * time.Second)
		}
		if tailscaleExe() == "" {
			return errors.New("Tailscale is not installed")
		}
	}
	ts := tailscaleExe()
	for i := 0; i < 20 && tsBackendState(ts) == ""; i++ { // the Tailscale service is starting
		time.Sleep(time.Second)
	}
	if tsBackendState(ts) == "Running" {
		return nil
	}
	fmt.Println("    sign in to Tailscale in the browser (a free account; Google or Microsoft work)...")
	c := exec.Command(ts, "login")
	hideWindow(c)
	pipe, _ := c.StdoutPipe()
	c.Stderr = c.Stdout
	if err := c.Start(); err != nil {
		return err
	}
	go func() {
		sc := bufio.NewScanner(pipe)
		opened := false
		for sc.Scan() {
			if u := tsURLRe.FindString(sc.Text()); u != "" && !opened {
				opened = true
				openURL(u)
				fmt.Println("    if the browser did not open, visit:", u)
			}
		}
	}()
	for i := 0; i < 200; i++ { // 10 minutes
		if tsBackendState(ts) == "Running" {
			c.Process.Kill()
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	c.Process.Kill()
	return errors.New("the Tailscale sign-in did not finish within 10 minutes")
}

// setupService installs the boot service (one UAC prompt) and waits until it answers.
func setupService() error {
	port := loadLinkCfg().Port
	health := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !phoneHealth(health, time.Second) {
		if err := elevateSelf("install-link"); err != nil {
			return fmt.Errorf("the permission prompt was declined or failed: %v", err)
		}
	}
	for i := 0; i < 30; i++ {
		if phoneHealth(health, time.Second) {
			return nil
		}
		time.Sleep(time.Second)
	}
	return errors.New("the service is installed but does not answer on port " + fmt.Sprint(port))
}

// setupFunnel allows Funnel for this tailnet when needed (browser page), switches TokenSaver
// to it and returns the connector address.
func setupFunnel() (string, error) {
	ts := tailscaleExe()
	port := loadLinkCfg().Port
	opened := false
	for i := 0; i < 60; i++ { // up to ~10 minutes for the user to allow Funnel
		if _, err := funnelEnsure(ts, port); err == nil {
			break
		}
		out, _ := runQuiet(15*time.Second, ts, "funnel", "--bg", "--https=443", fmt.Sprintf("http://127.0.0.1:%d", port))
		if u := tsURLRe.FindString(out); u != "" && !opened {
			opened = true
			openURL(u)
			fmt.Println("    allow Funnel in the Tailscale page that opened (one click), then wait here")
			fmt.Println("    if the browser did not open, visit:", u)
		}
		time.Sleep(10 * time.Second)
	}
	if msg := tunnelUseFunnel(); msg != "" {
		return "", errors.New(msg)
	}
	u := connectorURL()
	if u == "" {
		return "", errors.New("the address is not up yet")
	}
	return u, nil
}

// perplexityAppID finds the Perplexity desktop app's start ID, to open it when it is closed.
func perplexityAppID() string {
	out, _ := runQuiet(20*time.Second, psExe(), "-NoProfile", "-NonInteractive", "-Command",
		"(Get-StartApps | Where-Object { $_.Name -eq 'Perplexity' } | Select-Object -First 1).AppID")
	return strings.TrimSpace(out)
}

// perplexityAddConnector opens Perplexity's Add MCP connector form and fills it in; with
// cancel it presses Cancel instead of Add (a test that changes nothing).
func perplexityAddConnector(url string, cancel bool) error {
	if _, err := findWindow("Perplexity"); err != nil {
		id := perplexityAppID()
		if id == "" {
			return errors.New("the Perplexity app is not installed (get it from perplexity.ai/download and sign in)")
		}
		exec.Command("explorer.exe", `shell:AppsFolder\`+id).Start()
		for i := 0; i < 30; i++ {
			time.Sleep(time.Second)
			if _, err := findWindow("Perplexity"); err == nil {
				break
			}
		}
		time.Sleep(5 * time.Second)
	}
	text := func() string {
		s, _ := toolWindow(jsonRaw(map[string]any{"action": "text", "window": "Perplexity", "max_chars": 400000}))
		return s
	}
	if !cancel && strings.Contains(text(), url) {
		return nil // already added
	}
	do := func(steps ...map[string]any) error {
		out, err := toolWindow(jsonRaw(map[string]any{"action": "do", "window": "Perplexity", "steps": steps}))
		if err == nil && strings.Contains(out, "failed:") {
			err = errors.New(strings.TrimSpace(out))
		}
		return err
	}
	wait := map[string]any{"wait": 1.5}
	if err := do(map[string]any{"click": "Plugins"}, wait); err != nil {
		return fmt.Errorf("could not open Plugins (is Perplexity signed in?): %v", err)
	}
	if err := do(map[string]any{"click": "Add custom"}, wait, map[string]any{"click": "Add MCP connector"}, wait); err != nil {
		return fmt.Errorf("could not open Add MCP connector: %v", err)
	}
	if !strings.Contains(text(), "MCP server URL") {
		return errors.New("the Add MCP connector form did not open")
	}
	if err := do(
		map[string]any{"type": connectorName, "into": "Name"},
		map[string]any{"type": url, "into": "MCP server URL"},
		map[string]any{"click": "I understand custom connectors can introduce risks."},
	); err != nil {
		do(map[string]any{"click": "Cancel"})
		return fmt.Errorf("could not fill in the form: %v", err)
	}
	if !strings.Contains(text(), url) {
		do(map[string]any{"click": "Cancel"})
		return errors.New("the address did not land in the form's MCP server URL field")
	}
	if cancel {
		return do(map[string]any{"click": "Cancel"})
	}
	if err := do(map[string]any{"click": "Add"}, map[string]any{"wait": 3}); err != nil {
		return fmt.Errorf("could not press Add: %v", err)
	}
	if strings.Contains(text(), "MCP server URL") {
		do(map[string]any{"click": "Cancel"})
		return errors.New("Perplexity did not accept the form")
	}
	return nil
}

// setupWriteHowTo puts the short how-to on the Desktop and returns its path ("" on failure).
func setupWriteHowTo(url string) string {
	h := userHome()
	if h == "" {
		return ""
	}
	p := filepath.Join(h, "Desktop", "TokenSaver for Perplexity - how to use.txt")
	s := "TokenSaver lets Perplexity code on this PC and answers its \"yes?\" questions for you.\r\n\r\n" +
		"START: open a NEW Perplexity chat with the TokenSaver connector on and write, for example:\r\n" +
		"  Use TokenSaver: turn on project mode all. Then make me a to-do app in C:\\Projects\\todo\r\n\r\n" +
		"  project mode all            = Perplexity may do anything on this PC without asking\r\n" +
		"  project mode for <folder>   = only inside that folder\r\n\r\n" +
		"STOP: Use TokenSaver: turn off project mode   (it also stops after 8 hours or when Perplexity closes)\r\n\r\n" +
		"Keep the Perplexity app open while it works (minimised is fine).\r\n" +
		"After a restart, sign in to Windows; then Perplexity can use this PC again.\r\n" +
		"Auto-yes reads Perplexity in English; set Perplexity's language to English.\r\n\r\n" +
		"From a terminal the same switch is: tokensaver project all | tokensaver project <folder> | tokensaver project off\r\n\r\n" +
		"Connector address (keep it private, whoever has it can use this PC):\r\n  " + url + "\r\n"
	if os.WriteFile(p, []byte(s), 0o644) != nil {
		return ""
	}
	return p
}
