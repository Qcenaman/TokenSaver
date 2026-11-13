package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPipelineStopsAtFirstFailure(t *testing.T) {
	ran := []string{}
	step := func(name string, r pipeResult) pipeFn {
		return pipeFn{name, func() pipeResult { ran = append(ran, name); return r }}
	}
	steps, failAt, errText := runPipeline([]pipeFn{
		step("check review", pipeResult{detail: "verdict PASS"}),
		step("lint", pipeResult{detail: "0 errors"}),
		step("build", pipeResult{failShort: "build or unit tests failed", failDetail: "e: Main.kt:3 Unresolved reference: x"}),
		step("emulator test", pipeResult{detail: "never"}),
		step("git push", pipeResult{detail: "never"}),
	})
	if failAt != 2 || strings.Join(ran, ",") != "check review,lint,build" {
		t.Fatalf("failAt=%d ran=%v", failAt, ran)
	}
	rep := pipelineReport(`D:\App`, steps, failAt, errText)
	for _, want := range []string{"build_app FAILED at step 3 (build)", "1. check review: DONE", "3. build: NOT DONE — failed: build or unit tests failed",
		"4. emulator test: NOT DONE — not run: stopped at build", "5. git push: NOT DONE", "Unresolved reference: x"} {
		if !strings.Contains(rep, want) {
			t.Errorf("report misses %q:\n%s", want, rep)
		}
	}
	if strings.Contains(rep, "OK") {
		t.Errorf("a failed run must not say OK:\n%s", rep)
	}
}

func TestPipelineOKOnlyWhenNothingFailed(t *testing.T) {
	steps, failAt, errText := runPipeline([]pipeFn{
		{"build", func() pipeResult { return pipeResult{detail: "built"} }},
		{"git push", func() pipeResult { return pipeResult{skipped: true, detail: "not asked for"} }},
	})
	rep := pipelineReport("/app", steps, failAt, errText)
	if failAt != -1 || !strings.HasPrefix(rep, "build_app OK") || !strings.Contains(rep, "2. git push: NOT DONE — not asked for") {
		t.Errorf("report:\n%s", rep)
	}
}

func TestAndroidAppID(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "app"), 0o755)
	os.WriteFile(filepath.Join(root, "app", "build.gradle.kts"), []byte("android {\n    namespace = \"com.x.ns\"\n    defaultConfig {\n        applicationId = \"com.x.app\"\n    }\n}\n"), 0o644)
	if got := androidAppID(root); got != "com.x.app" {
		t.Errorf("app id = %q", got)
	}
	if got := androidAppID(t.TempDir()); got != "" {
		t.Errorf("no build file: %q", got)
	}
}

func TestBuildAppNeedsAFolder(t *testing.T) {
	if _, err := toolBuildApp(json.RawMessage(`{}`)); err == nil {
		t.Error("no path: want an error")
	}
	if _, err := toolBuildApp(jsonRaw(map[string]any{"path": filepath.Join(t.TempDir(), "missing")})); err == nil {
		t.Error("missing folder: want an error")
	}
}

func TestToolAnnotationsAreTrue(t *testing.T) {
	for _, d := range platformToolDefs() {
		var def struct {
			Name        string `json:"name"`
			Annotations *struct {
				ReadOnly    bool  `json:"readOnlyHint"`
				Destructive *bool `json:"destructiveHint"`
			} `json:"annotations"`
		}
		if err := json.Unmarshal(d, &def); err != nil {
			t.Fatal(err)
		}
		safe := def.Annotations != nil && def.Annotations.Destructive != nil && !*def.Annotations.Destructive
		if safe != nonDestructiveTools[def.Name] {
			t.Errorf("%s: destructiveHint=false is %v, want %v", def.Name, safe, nonDestructiveTools[def.Name])
		}
		if safe && def.Annotations.ReadOnly {
			t.Errorf("%s: a writing tool is not read-only", def.Name)
		}
	}
	for _, n := range []string{"run", "run_job", "saved", "system", "window", "browser", "office", "rag", "job", "phone", "peer"} {
		if nonDestructiveTools[n] || readOnlyTools[n] {
			t.Errorf("%s can delete or overwrite and must stay unlabelled", n)
		}
	}
}

func TestConfirmQuestion(t *testing.T) {
	page := func(answer, after string) string {
		return "Perplexity.exe Perplexity\n[New Ctrl+N]  [Answer]\nbuild my app  4:40 AM  [Edit query]  [Copy query]\n" + answer + "  [Copy]  [Helpful]  [Not helpful]  [More actions]\n" + after + "[Add]  [Search]  [Model]  [Dictation]\n"
	}
	for name, c := range map[string]struct {
		text string
		ask  bool
	}{
		"type yes":         {page("I will now write 6 files with run_job. Type yes to continue.", ""), true},
		"reply yes":        {page("Next I push to GitHub. Reply \"yes\" to proceed", ""), true},
		"shall I proceed":  {page("The build passed. Shall I proceed with the emulator test?", ""), true},
		"do you want me":   {page("Lint is clean. Do you want me to build the APK now?", ""), true},
		"plain answer":     {page("The APK is at app/build/outputs/apk/debug/app-debug.apk.", ""), false},
		"still answering":  {page("Shall I proceed with the build?", "[Stop response (Esc)]  "), false},
		"user wrote since": {page("Shall I proceed with the build?", "yes  4:41 AM  [Edit query]  [Copy query]\n"), false},
		"no answer yet":    {"Perplexity\nbuild my app  [Edit query]  [Copy query]\n[Working 10s]\n", false},
		"action card": {"Perplexity\nmake an app  4:01 PM  [Edit query]  [Copy query]\n[Finished 27s]\n[Projects]  Test the app\n" +
			"Android app  Commit the appCommit the new to-do app files (todo.py, README.md, .gitignore) in C:\\code\\app with the message “Add app”?\n" +
			"[History]\nCreate an Automation  {\"action\": \"commit\", \"path\": \"C:\\\\code\\\\app\"}\n[Fork]\nApi key  Ask a follow-up\n[Search]  [Submit]\n", true},
	} {
		q, _, key := confirmQuestion(c.text)
		if (q != "") != c.ask || (key != "") != c.ask {
			t.Errorf("%s: question=%q key=%q, want a question: %v", name, q, key, c.ask)
		}
	}
	_, _, k1 := confirmQuestion(page("Shall I proceed with the build?", ""))
	_, _, k2 := confirmQuestion(page("Shall I proceed with the build?", ""))
	_, _, k3 := confirmQuestion(page("Shall I proceed with the push?", ""))
	if k1 != k2 || k1 == k3 {
		t.Error("the key must be the same for the same message and differ for another")
	}
	_, around, _ := confirmQuestion(page("This will delete the old build folder. Shall I proceed?", ""))
	if !ayRisky.MatchString(around) {
		t.Error("a go-ahead about deleting is left for the user")
	}
	_, around, _ = confirmQuestion(page("The build passed. Shall I proceed with the emulator test?", ""))
	if ayRisky.MatchString(around) {
		t.Error("an ordinary go-ahead is answered")
	}
}
