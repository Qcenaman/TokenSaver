//go:build !linux

package main

// Coding commands for the local model, each answering in as few lines as possible:
// test (one test by name), outline (symbols with line numbers instead of whole files),
// export (many files in one call, or a project as a zip), new (a project the way the
// user makes them), deps (list/add/update/remove dependencies) and logcat (crash lines
// of one app on the phone).

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func projectRoot(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("needs path (project folder)")
	}
	r, err := expandPath(p)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(r)
	if err != nil {
		return "", fmt.Errorf("path not found: %s", r)
	}
	if !st.IsDir() {
		r = filepath.Dir(r)
	}
	return r, nil
}

// ---------- test ----------

func toolTest(a map[string]any) (string, error) {
	root, err := projectRoot(str(a, "path"))
	if err != nil {
		return "", err
	}
	name := str(a, "name", "test", "filter")
	has := func(n string) bool { return exists(filepath.Join(root, n)) }
	var s buildStep
	switch {
	case has("go.mod"):
		args := []string{"test", "-count=1", "./..."}
		if name != "" {
			args = append(args, "-run", name)
		}
		s = buildStep{"go test", root, nil, "go", args}
	case has("Cargo.toml"):
		s = buildStep{"cargo test", root, nil, "cargo", []string{"test", "-q", name}}
	case has("gradlew") || has("gradlew.bat"):
		args := []string{"testDebugUnitTest", "--console=plain", "-q"}
		if name != "" {
			args = append(args, "--tests", name)
		}
		s = buildStep{"gradle test", root, ckJavaEnv(), gradlew(root), args}
	case has("pubspec.yaml"):
		args := []string{"test"}
		if name != "" {
			args = append(args, "--plain-name", name)
		}
		s = buildStep{"flutter test", root, nil, "flutter", args}
	case has("package.json"):
		args := []string{"test", "--silent"}
		if name != "" {
			args = append(args, "--", "-t", name)
		}
		s = buildStep{"npm test", root, []string{"CI=1"}, "npm", args}
	case has("pyproject.toml") || has("pytest.ini") || hasGlob(root, "test_*.py") || has("tests"):
		args := []string{"-m", "pytest", "-q", "-x", "--no-header", "-p", "no:cacheprovider"}
		if name != "" {
			args = append(args, "-k", name)
		}
		py := venvPython(root)
		s = buildStep{"pytest", root, nil, py, args}
	case has("CMakeLists.txt"):
		bd := filepath.Join(root, "build", hostTarget())
		if _, _, code, _ := ckRunEnv(nil, root, 10*time.Minute, "cmake", "-S", root, "-B", bd); code == 0 {
			ckRunEnv(nil, root, 20*time.Minute, "cmake", "--build", bd, "-j")
		}
		args := []string{"--test-dir", bd, "--output-on-failure"}
		if name != "" {
			args = append(args, "-R", name)
		}
		s = buildStep{"ctest", root, nil, "ctest", args}
	default:
		return "", fmt.Errorf("no tests found in %s", root)
	}
	start := time.Now()
	so, se, code, err := ckRunEnv(s.env, s.dir, 20*time.Minute, s.name, s.args...)
	out := so + "\n" + se
	if err != nil || code != 0 {
		lines := buildErrLines(out, 30)
		if len(lines) == 0 {
			lines = strings.Split(ckFirstLines(strings.TrimSpace(out), 30), "\n")
		}
		why := fmt.Sprintf("exit %d", code)
		if err != nil {
			why = err.Error()
		}
		return fmt.Sprintf("✗ %s: %s\n  %s", s.label, why, strings.Join(lines, "\n  ")), nil
	}
	note := ""
	if m := regexp.MustCompile(`(\d+ passed|test result: ok\. \d+ passed|Tests:\s+\d+ passed|\d+ tests? passed)`).FindString(out); m != "" {
		note = ", " + m
	} else if n := strings.Count("\n"+so, "\nok "); n > 0 {
		note = fmt.Sprintf(", %d package(s) ok", n)
	}
	return fmt.Sprintf("✓ %s (%s%s)", s.label, time.Since(start).Round(time.Second), note), nil
}

// ---------- export ----------

// toolExport writes files:{relative path: text} under path in one job (undoable), or
// zips the project at path (zip:true; to = the .zip) without git, dependency and build folders.
func toolExport(a map[string]any) (string, []toolImage, error) {
	root, err := expandPath(str(a, "path", "to", "folder"))
	if err != nil {
		return "", nil, err
	}
	if fs, ok := a["files"].(map[string]any); ok && len(fs) > 0 {
		var steps []map[string]any
		names := make([]string, 0, len(fs))
		for n := range fs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			c, _ := fs[n].(string)
			rel := filepath.Clean(strings.ReplaceAll(n, `\`, "/"))
			if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
				return "", nil, fmt.Errorf("file names must be relative to path: %s", n)
			}
			steps = append(steps, map[string]any{"op": "write", "to": filepath.Join(root, rel), "content": c})
		}
		return compactJob(steps, a)
	}
	if a["zip"] != true && str(a, "zip") == "" {
		return "", nil, errors.New(`export needs files:{"name":"text",...} (written under path) or zip:true (pack the folder)`)
	}
	to := str(a, "zip")
	if to == "" || to == "true" {
		to = str(a, "out")
	}
	if to == "" {
		to = root + ".zip"
	}
	if to, err = expandPath(to); err != nil {
		return "", nil, err
	}
	n, size, err := zipProject(root, to)
	if err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("%s (%d files, %s)", to, n, hb(size)), nil, nil
}

func zipProject(root, to string) (int, int64, error) {
	if err := checkAllowed(to); err != nil {
		return 0, 0, err
	}
	ign := gitIgnored(root)
	tmp := to + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, 0, err
	}
	zw := zip.NewWriter(f)
	n := 0
	werr := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if fi.IsDir() {
			if p != root && (fi.Name() == ".git" || fi.Name() == "node_modules" || fi.Name() == ".gradle" || fi.Name() == ".venv" || isBuildOutDir(fi.Name(), p) || ign[pathKey(p)]) {
				return filepath.SkipDir
			}
			return nil
		}
		if ign[pathKey(p)] || samePath(p, to) || samePath(p, tmp) || !fi.Mode().IsRegular() {
			return nil
		}
		h, _ := zip.FileInfoHeader(fi)
		h.Name, h.Method = filepath.ToSlash(rel), zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		src, err := os.Open(p)
		if err != nil {
			return nil
		}
		_, err = io.Copy(w, src)
		src.Close()
		n++
		return err
	})
	cerr := zw.Close()
	f.Close()
	if werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(tmp)
		return 0, 0, werr
	}
	if err := os.Rename(tmp, to); err != nil {
		return 0, 0, err
	}
	st, _ := os.Stat(to)
	return n, st.Size(), nil
}

// ---------- logcat ----------

// toolLogcat: the error and crash lines one app logged on the phone (over adb).
func toolLogcat(a map[string]any) (string, error) {
	c := loadPhoneConf()
	if d := str(a, "device"); d != "" {
		adbRun(10*time.Second, "connect", d)
		c.AdbSerial = d
	}
	s, err := phoneAdbSerial(c)
	if err != nil {
		return "", fmt.Errorf("%v (logcat needs adb: wireless debugging on, then device: IP:port)", err)
	}
	app := str(a, "app", "package", "name")
	n := intArg(a, "lines")
	if n <= 0 {
		n = 40
	}
	pkg := ""
	if app != "" {
		if strings.Count(app, ".") >= 1 && !strings.Contains(app, " ") {
			pkg = app
		} else {
			out, _ := adbRun(15*time.Second, "-s", s, "shell", "pm", "list", "packages")
			for _, l := range strings.Split(out, "\n") {
				p := strings.TrimSpace(strings.TrimPrefix(l, "package:"))
				if strings.Contains(strings.ToLower(p), strings.ToLower(strings.ReplaceAll(app, " ", ""))) {
					pkg = p
					break
				}
			}
			if pkg == "" {
				return "", fmt.Errorf("no installed app matches %q", app)
			}
		}
	}
	var b strings.Builder
	if crash, _ := adbRun(15*time.Second, "-s", s, "logcat", "-d", "-b", "crash", "-t", "200"); strings.TrimSpace(crash) != "" {
		var keep []string
		for _, l := range strings.Split(crash, "\n") {
			if pkg == "" || strings.Contains(crash, pkg) {
				keep = append(keep, strings.TrimSpace(l))
			}
		}
		if len(keep) > 0 {
			if len(keep) > n {
				keep = keep[len(keep)-n:]
			}
			b.WriteString("crashes:\n" + strings.Join(keep, "\n") + "\n")
		}
	}
	args := []string{"-s", s, "logcat", "-d", "-t", "2000", "*:E"}
	if pkg != "" {
		pid, _ := adbRun(10*time.Second, "-s", s, "shell", "pidof", pkg)
		if pid = strings.TrimSpace(pid); pid != "" {
			args = append(args, "--pid="+strings.Fields(pid)[0])
		} else if b.Len() == 0 {
			return pkg + " isn't running and has no crash lines", nil
		}
	}
	out, err := adbRun(30*time.Second, args...)
	if err != nil && out == "" {
		return "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" && !strings.HasPrefix(lines[0], "---------") {
		b.WriteString("errors:\n" + strings.Join(lines, "\n"))
	}
	if b.Len() == 0 {
		return "no errors logged", nil
	}
	return strings.TrimSpace(b.String()), nil
}
