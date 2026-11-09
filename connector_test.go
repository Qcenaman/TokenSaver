package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandPathHomeTokens(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TOKENSAVER_USER_HOME", home)
	sub := filepath.Join(home, "Projects", "app")
	cases := map[string]string{
		"$HOME": home, "~": home, "%USERPROFILE%": home, "$env:USERPROFILE": home, "${HOME}": home, "${env:USERPROFILE}": home,
		"$HOME/Projects/app": sub, "~/Projects/app": sub, "%USERPROFILE%/Projects/app": sub, "$env:USERPROFILE/Projects/app": sub,
	}
	if isWin {
		cases[`~\Projects\app`] = sub
		cases[`$env:USERPROFILE\Projects\app`] = sub
		cases[`%USERPROFILE%\Projects\app`] = sub
	}
	for in, want := range cases {
		got, err := expandPath(in)
		if err != nil || !samePath(got, want) {
			t.Errorf("expandPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestExpandPathRelativeShowsExample(t *testing.T) {
	for _, in := range []string{"Projects/app", "$HOMEWORK/app", "~app"} {
		if _, err := expandPath(in); err == nil || !strings.Contains(err.Error(), absExample()) {
			t.Errorf("expandPath(%q): want an error with an example, got %v", in, err)
		}
	}
}

func TestReadOnlyAnnotations(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range platformToolDefs() {
		var def struct {
			Name        string `json:"name"`
			Annotations *struct {
				ReadOnlyHint bool `json:"readOnlyHint"`
			} `json:"annotations"`
		}
		if err := json.Unmarshal(d, &def); err != nil {
			t.Fatalf("tool definition is not valid JSON: %v", err)
		}
		seen[def.Name] = true
		if ro := def.Annotations != nil && def.Annotations.ReadOnlyHint; ro != readOnlyTools[def.Name] {
			t.Errorf("%s: readOnlyHint = %v, want %v", def.Name, ro, readOnlyTools[def.Name])
		}
	}
	for n := range readOnlyTools {
		if !seen[n] {
			t.Errorf("read-only tool %s is not in the tool list", n)
		}
	}
	for _, n := range []string{"run", "run_job", "git", "check", "office", "window", "browser", "phone", "job", "saved", "system", "rag", "peer"} {
		if readOnlyTools[n] {
			t.Errorf("%s can change things and must not be labelled read-only", n)
		}
	}
}

func TestRunSteersFileWritesToRunJob(t *testing.T) {
	const want = "use run_job write/edit steps, one step per file"
	if !strings.Contains(serverInstructions, want) {
		t.Error("server instructions don't steer file writes to run_job")
	}
	for _, d := range toolDefs {
		var def struct {
			Name, Description string
		}
		json.Unmarshal(d, &def)
		if def.Name == "run" && !strings.Contains(def.Description, want) {
			t.Error("run's description doesn't steer file writes to run_job")
		}
	}
}

func TestAndroidProjectWithoutWrapper(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{
		"settings.gradle.kts":              "rootProject.name = \"WifiScanner\"\ninclude(\":app\")\n",
		"build.gradle.kts":                 "plugins {\n    id(\"com.android.application\") version \"8.7.3\" apply false\n}\n",
		"app/build.gradle.kts":             "plugins {\n    id(\"com.android.application\")\n}\n",
		"app/src/main/AndroidManifest.xml": "<manifest/>\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !ckAndroidProject(root) {
		t.Error("a Gradle project without the wrapper is still an Android project")
	}
	if ckIsAndroid(root) {
		t.Error("without the wrapper Android lint can't run")
	}
	if ckAndroidProject(t.TempDir()) {
		t.Error("an empty folder is not an Android project")
	}
}

func TestGradleOfflineMiss(t *testing.T) {
	for out, want := range map[string]bool{
		"Plugin [id: 'com.android.application', version: '8.7.3', apply: false] was not found in any of the following sources:": true,
		"No cached version of junit:junit:4.13.2 available for offline mode.":                                                   true,
		"e: MainActivity.kt:12:5 Unresolved reference: foo":                                                                     false,
	} {
		if gradleOfflineMiss(out) != want {
			t.Errorf("gradleOfflineMiss(%q) != %v", out, want)
		}
	}
	if got := dropArg([]string{"assembleDebug", "-q", "--offline"}, "--offline"); len(got) != 2 {
		t.Errorf("dropArg left %v", got)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestCheckNewStartsProject(t *testing.T) {
	parent := t.TempDir()
	out, _, err := callTool("check", mustJSON(map[string]any{"action": "new", "kind": "go", "name": "demo", "path": parent}))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"go.mod", "main.go", "main_test.go", ".github/workflows/test.yml"} {
		if !exists(filepath.Join(parent, "demo", filepath.FromSlash(f))) {
			t.Errorf("%s missing; output: %s", f, out)
		}
	}
	if strings.Contains(out, "pushed to") {
		t.Error("no GitHub repo unless repo:true")
	}
}
