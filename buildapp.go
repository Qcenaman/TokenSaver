package main

// check action:build_app: the whole way from code to a pushed app in one call, so a chat app
// that asks before every call asks once. Steps, in order, stopping at the first that fails:
// check review -> lint -> build -> emulator test -> git push (only with push:true).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type pipeStep struct{ Name, Status, Detail string }

// pipeResult is what one step reports: detail when done, skipped when it was not asked for or
// does not apply, failShort (+ the exact error in failDetail) when it failed.
type pipeResult struct {
	detail, failShort, failDetail string
	skipped                       bool
}

type pipeFn struct {
	name string
	run  func() pipeResult
}

// runPipeline runs the steps in order and stops at the first failure; what comes after it is
// reported as not run.
func runPipeline(fns []pipeFn) (steps []pipeStep, failAt int, errText string) {
	failAt = -1
	for i, f := range fns {
		if failAt >= 0 {
			steps = append(steps, pipeStep{f.name, "NOT DONE", "not run: stopped at " + fns[failAt].name})
			continue
		}
		r := f.run()
		switch {
		case r.failShort != "":
			steps = append(steps, pipeStep{f.name, "NOT DONE", "failed: " + r.failShort})
			failAt, errText = i, r.failDetail
		case r.skipped:
			steps = append(steps, pipeStep{f.name, "NOT DONE", r.detail})
		default:
			steps = append(steps, pipeStep{f.name, "DONE", r.detail})
		}
	}
	return steps, failAt, errText
}

// pipelineReport never says OK unless nothing failed.
func pipelineReport(root string, steps []pipeStep, failAt int, errText string) string {
	var b strings.Builder
	if failAt >= 0 {
		fmt.Fprintf(&b, "build_app FAILED at step %d (%s): %s\n", failAt+1, steps[failAt].Name, root)
	} else {
		fmt.Fprintf(&b, "build_app OK, every requested step passed: %s\n", root)
	}
	for i, s := range steps {
		fmt.Fprintf(&b, "%d. %s: %s", i+1, s.Name, s.Status)
		if s.Detail != "" {
			b.WriteString(" — " + s.Detail)
		}
		b.WriteString("\n")
	}
	if failAt >= 0 && strings.TrimSpace(errText) != "" {
		b.WriteString("Error:\n" + strings.TrimSpace(errText))
	}
	return strings.TrimSpace(b.String())
}

type buildAppArgs struct {
	Path   string `json:"path"`
	Push   bool   `json:"push"`
	Name   string `json:"name"`
	Public bool   `json:"public"`
	Wait   int    `json:"wait_seconds"`
}

type buildAppRun struct {
	done  chan struct{}
	out   string
	start time.Time
}

var buildAppRuns sync.Map // project folder -> *buildAppRun

func jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// toolBuildApp starts the pipeline (or joins the one already running for that folder) and
// waits for it; a long run answers "still running" and the same call again collects the result.
func toolBuildApp(raw json.RawMessage) (string, error) {
	var a buildAppArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("build_app needs path (the project folder)")
	}
	root, err := expandPath(a.Path)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return "", fmt.Errorf("folder not found: %s", root)
	}
	if err := checkAllowed(root); err != nil {
		return "", err
	}
	key := normCase(root)
	fresh := &buildAppRun{done: make(chan struct{}), start: time.Now()}
	r, running := buildAppRuns.LoadOrStore(key, fresh)
	run, ok := r.(*buildAppRun)
	if !ok {
		return "", errors.New("internal error: unexpected build_app state")
	}
	if !running {
		go func() {
			defer close(run.done)
			defer func() {
				if p := recover(); p != nil {
					run.out = fmt.Sprintf("build_app FAILED: internal error: %v", p)
				}
			}()
			run.out = buildAppPipeline(root, a)
		}()
	}
	wait := 240 * time.Second
	if a.Wait > 0 {
		wait = time.Duration(min(a.Wait, 900)) * time.Second
	}
	select {
	case <-run.done:
		buildAppRuns.Delete(key)
		return run.out, nil
	case <-time.After(wait):
		return fmt.Sprintf("build_app is still running for %s (%s so far). Nothing is finished or pushed yet. Call check action:build_app with the same path again to get the result; that does not start a second run.",
			root, time.Since(run.start).Round(time.Second)), nil
	}
}

var (
	baVerdictRe = regexp.MustCompile(`(?m)^Verdict: (\w+)([^\n]*)`)
	baApkRe     = regexp.MustCompile(`(?m)^APK: (.+)$`)
	baAppIDRe   = regexp.MustCompile(`(?m)^\s*applicationId\s*=?\s*["']([\w.]+)["']`)
	baNSRe      = regexp.MustCompile(`(?m)^\s*namespace\s*=?\s*["']([\w.]+)["']`)
)

func buildAppPipeline(root string, a buildAppArgs) string {
	android := ckAndroidProject(root)
	apk := ""
	steps, failAt, errText := runPipeline([]pipeFn{
		{"check review", func() pipeResult {
			out, err := toolReview(jsonRaw(map[string]any{"path": root, "no_test": true}))
			if err != nil {
				return pipeResult{failShort: "the review could not run", failDetail: err.Error()}
			}
			m := baVerdictRe.FindStringSubmatch(out)
			if m == nil {
				return pipeResult{failShort: "the review gave no verdict", failDetail: clip(out, 2000)}
			}
			if m[1] == "BLOCK" {
				return pipeResult{failShort: "review verdict BLOCK", failDetail: clip(out, 2500)}
			}
			return pipeResult{detail: "verdict " + m[1] + clip(m[2], 120)}
		}},
		{"lint", func() pipeResult { return baLint(root, android) }},
		{"build", func() pipeResult {
			out, err := toolBuild(jsonRaw(map[string]any{"path": root}))
			if err != nil {
				return pipeResult{failShort: "the build could not start", failDetail: err.Error()}
			}
			if strings.Contains(out, "✗") {
				return pipeResult{failShort: "build or unit tests failed", failDetail: clip(out, 2500)}
			}
			if m := baApkRe.FindStringSubmatch(out); m != nil {
				apk = strings.TrimSpace(m[1])
				return pipeResult{detail: "built, unit tests passed, debug APK " + apk}
			}
			return pipeResult{detail: "built, tests passed"}
		}},
		{"emulator test", func() pipeResult {
			if !android {
				return pipeResult{skipped: true, detail: "not an Android project, nothing to run on the emulator"}
			}
			return baEmulator(root, apk)
		}},
		{"git push", func() pipeResult {
			if !a.Push {
				return pipeResult{skipped: true, detail: "not asked for (push:true pushes to GitHub)"}
			}
			return baPush(root, a, android)
		}},
	})
	return pipelineReport(root, steps, failAt, errText)
}

func baLint(root string, android bool) pipeResult {
	if !android {
		out, err := toolCodeCheck(jsonRaw(map[string]any{"path": root}))
		if err != nil {
			return pipeResult{failShort: "the linters could not run", failDetail: err.Error()}
		}
		first, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
		return pipeResult{detail: clip(first, 160)}
	}
	if !hasGradlew(root) {
		if err := addGradleWrapper(root); err != nil {
			return pipeResult{failShort: "no Gradle wrapper and adding one failed", failDetail: err.Error()}
		}
	}
	issues, fail, _ := ckAndroid(root)
	var errs []string
	warns := 0
	for _, is := range issues {
		if is.Warn {
			warns++
		} else if len(errs) < 15 {
			errs = append(errs, fmt.Sprintf("%s:%d %s %s", is.File, is.Line, is.Code, is.Msg))
		}
	}
	if len(errs) > 0 {
		return pipeResult{failShort: "Android lint found errors", failDetail: strings.Join(errs, "\n")}
	}
	if fail != "" && len(issues) == 0 {
		return pipeResult{failShort: "Android lint could not finish", failDetail: fail}
	}
	return pipeResult{detail: fmt.Sprintf("Android lint: 0 errors, %d warning(s)", warns)}
}

// androidAppID reads the app's package name from app/build.gradle(.kts).
func androidAppID(root string) string {
	for _, f := range []string{"app/build.gradle.kts", "app/build.gradle"} {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		if m := baAppIDRe.FindSubmatch(b); m != nil {
			return string(m[1])
		}
		if m := baNSRe.FindSubmatch(b); m != nil {
			return string(m[1])
		}
	}
	return ""
}

func baAdbHas(serial string) bool {
	out, _ := adbRun(8*time.Second, "devices")
	return adbStates(out)[serial] == "device"
}

// baScript runs an emulator start/stop script; its output goes to a file so a program the
// script leaves running (the emulator) can't keep this call waiting.
func baScript(script string, timeout time.Duration) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	log := filepath.Join(os.TempDir(), "tokensaver-emulator.log")
	f, err := os.Create(log)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	var c *exec.Cmd
	if isWin {
		c = exec.CommandContext(ctx, "cmd", "/c", script)
	} else {
		c = exec.CommandContext(ctx, "sh", script)
	}
	c.Stdout, c.Stderr = f, f
	_ = c.Run() // what it printed is the report; a failed start shows as a missing emulator
	b, _ := os.ReadFile(log)
	return clip(strings.TrimSpace(string(b)), 600)
}

// baEmulator installs the APK on the test phone (starting it when needed), launches the app
// and reads logcat for a crash. An emulator it started is stopped again.
func baEmulator(root, apk string) pipeResult {
	serial := cfg.EmulatorSerial
	if serial == "" {
		serial = "emulator-5554"
	}
	start, stop := cfg.EmulatorStart, cfg.EmulatorStop
	if isWin {
		if start == "" {
			start = `D:\Claude Apps\Emulator\start-emulator.cmd`
		}
		if stop == "" {
			stop = `D:\Claude Apps\Emulator\stop-emulator.cmd`
		}
	}
	if apk == "" {
		if m, _ := filepath.Glob(filepath.Join(root, "*", "build", "outputs", "apk", "debug", "*.apk")); len(m) > 0 {
			apk = m[0]
		}
	}
	if apk == "" {
		return pipeResult{failShort: "no debug APK to install", failDetail: "the build left no APK under */build/outputs/apk/debug"}
	}
	pkg := androidAppID(root)
	if pkg == "" {
		return pipeResult{failShort: "app id not found", failDetail: "no applicationId or namespace in app/build.gradle(.kts)"}
	}
	started := false
	if !baAdbHas(serial) {
		if start == "" || !exists(start) {
			return pipeResult{failShort: serial + " is not running", failDetail: "no emulator start script (emulator_start in config.json)"}
		}
		out := baScript(start, 8*time.Minute)
		if !baAdbHas(serial) {
			return pipeResult{failShort: "the emulator did not come up", failDetail: out}
		}
		started = true
	}
	stopNote := ""
	if started {
		stopNote = ", emulator stopped again"
		defer func() {
			if stop != "" && exists(stop) {
				baScript(stop, time.Minute)
			} else {
				adbRun(20*time.Second, "-s", serial, "emu", "kill")
			}
		}()
	}
	if out, _ := adbRun(4*time.Minute, "-s", serial, "install", "-r", apk); !strings.Contains(out, "Success") {
		return pipeResult{failShort: "install on " + serial + " failed", failDetail: clip(strings.TrimSpace(out), 1200)}
	}
	adbRun(10*time.Second, "-s", serial, "logcat", "-c")
	if out, _ := adbShell(serial, 30*time.Second, "monkey -p "+pkg+" -c android.intent.category.LAUNCHER 1"); strings.Contains(out, "No activities found") {
		return pipeResult{failShort: "the app has no launcher activity", failDetail: clip(strings.TrimSpace(out), 600)}
	}
	time.Sleep(8 * time.Second)
	crash, _ := adbRun(20*time.Second, "-s", serial, "logcat", "-d", "-b", "crash")
	if strings.Contains(crash, pkg) {
		lines := strings.Split(strings.TrimSpace(crash), "\n")
		if len(lines) > 20 {
			lines = lines[:20]
		}
		return pipeResult{failShort: "the app crashed on launch", failDetail: strings.Join(lines, "\n")}
	}
	if pid, _ := adbShell(serial, 10*time.Second, "pidof "+pkg); strings.TrimSpace(pid) == "" {
		return pipeResult{failShort: "the app is not running 8 s after launch", failDetail: "no crash in logcat, but " + pkg + " has no process"}
	}
	return pipeResult{detail: "installed on " + serial + ", launched " + pkg + ", no crash in logcat" + stopNote}
}

// baPush commits what is there and pushes it to github.com/<user>/<name>, making the repo when missing.
func baPush(root string, a buildAppArgs, android bool) pipeResult {
	fail := func(short, detail string) pipeResult {
		return pipeResult{failShort: short, failDetail: clip(strings.TrimSpace(detail), 1200)}
	}
	name := strings.Trim(projNameRe.ReplaceAllString(a.Name, "-"), "-")
	if name == "" {
		name = strings.Trim(projNameRe.ReplaceAllString(filepath.Base(root), "-"), "-")
	}
	if !exists(filepath.Join(root, ".git")) {
		if out, code, _ := gitExec(root, 20*time.Second, "init", "-q", "-b", "main"); code != 0 {
			return fail("git init failed", out)
		}
	}
	if gi := filepath.Join(root, ".gitignore"); android && !exists(gi) {
		os.WriteFile(gi, []byte(".gradle/\nbuild/\n/app/build/\nlocal.properties\n.idea/\n*.iml\n"), 0o644)
	}
	gitExec(root, time.Minute, "add", "-A")
	gitExec(root, time.Minute, "commit", "-q", "-m", "Builds, passes its tests and runs on the emulator")
	hash, code, _ := gitExec(root, 20*time.Second, "rev-parse", "--short", "HEAD")
	hash = strings.TrimSpace(hash)
	if code != 0 || hash == "" {
		return fail("nothing could be committed", "git has no commit here (are git user.name and user.email set?)")
	}
	url, code, _ := gitExec(root, 20*time.Second, "remote", "get-url", "origin")
	url = strings.TrimSpace(url)
	if code != 0 || url == "" {
		user := ghUser()
		if user == "" {
			return fail("not signed in to GitHub", "sign in first (git action login), then call build_app again")
		}
		gh, _ := ghPath()
		repo := user + "/" + name
		url = "https://github.com/" + repo
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if exec.CommandContext(ctx, gh, "repo", "view", repo).Run() != nil {
			vis := "--private"
			if a.Public {
				vis = "--public"
			}
			if out, err := exec.CommandContext(ctx, gh, "repo", "create", repo, vis).CombinedOutput(); err != nil {
				return fail("could not create "+url, string(out))
			}
		}
		if out, code, _ := gitExec(root, 20*time.Second, "remote", "add", "origin", url+".git"); code != 0 {
			return fail("could not set the remote", out)
		}
	}
	if out, code, _ := gitExec(root, 5*time.Minute, "push", "-u", "origin", "HEAD"); code != 0 {
		return fail("git push was refused", out)
	}
	return pipeResult{detail: "commit " + hash + " pushed to " + strings.TrimSuffix(url, ".git")}
}
