package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The review tools are pinned: these exact builds were tested with the parsers below, and each
// download is checked against its SHA-256 before it is used. They live in <checker>/tools.

type rvAsset struct{ file, sha string }

type rvToolSpec struct {
	version, exe, url string // url: release download folder
	assets            map[string]rvAsset
}

var rvTools = map[string]rvToolSpec{
	"opengrep": {"1.33.1", "opengrep", "https://github.com/opengrep/opengrep/releases/download/v1.33.1/", map[string]rvAsset{
		"windows/amd64": {"opengrep_windows_x86.exe", "132e606e071bdd3928286fddc893198a6106a90820d46e21197e0ace1a23e0c1"},
		"linux/amd64":   {"opengrep_manylinux_x86", "46e8133cc490f9f50518061590ce2f6be5a08afaeda034471c0ceda4b3899808"},
		"linux/arm64":   {"opengrep_manylinux_aarch64", "d1433f63a90b8bc0b6ad4b498a13b372b856ce9a754a43484ba4920fe2d76dcf"},
	}},
	"golangci-lint": {"2.15.0", "golangci-lint", "https://github.com/golangci/golangci-lint/releases/download/v2.15.0/", map[string]rvAsset{
		"windows/amd64": {"golangci-lint-2.15.0-windows-amd64.zip", "cd7ea6063c95de245e17e5246e42bdc14bb221792c429dbd239ab3a6ade6ca6a"},
		"linux/amd64":   {"golangci-lint-2.15.0-linux-amd64.tar.gz", "9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea"},
		"linux/arm64":   {"golangci-lint-2.15.0-linux-arm64.tar.gz", "b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998"},
	}},
	"gitleaks": {"8.30.1", "gitleaks", "https://github.com/gitleaks/gitleaks/releases/download/v8.30.1/", map[string]rvAsset{
		"windows/amd64": {"gitleaks_8.30.1_windows_x64.zip", "d29144deff3a68aa93ced33dddf84b7fdc26070add4aa0f4513094c8332afc4e"},
		"linux/amd64":   {"gitleaks_8.30.1_linux_x64.tar.gz", "551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"},
		"linux/arm64":   {"gitleaks_8.30.1_linux_arm64.tar.gz", "e4a487ee7ccd7d3a7f7ec08657610aa3606637dab924210b3aee62570fb4b080"},
	}},
	"osv-scanner": {"2.7.0", "osv-scanner", "https://github.com/google/osv-scanner/releases/download/v2.7.0/", map[string]rvAsset{
		"windows/amd64": {"osv-scanner_windows_amd64.exe", "9964ed64ec9a387391eb4cc006ad5c531154821aef29b793e02c448de4686438"},
		"linux/amd64":   {"osv-scanner_linux_amd64", "160c6253e2922e9e0a6a9771c5c4c40a4b3c6090b34a3cdd5e80c96cc2e4218b"},
		"linux/arm64":   {"osv-scanner_linux_arm64", "4061ef4c021ed3c47f203b818f337e351336cc5cd67c0274fc36bd1dfc728dd7"},
	}},
	"actionlint": {"1.7.12", "actionlint", "https://github.com/rhysd/actionlint/releases/download/v1.7.12/", map[string]rvAsset{
		"windows/amd64": {"actionlint_1.7.12_windows_amd64.zip", "6e7241b51e6817ea6a047693d8e6fed13b31819c9a0dd6c5a726e1592d22f6e9"},
		"linux/amd64":   {"actionlint_1.7.12_linux_amd64.tar.gz", "8aca8db96f1b94770f1b0d72b6dddcb1ebb8123cb3712530b08cc387b349a3d8"},
		"linux/arm64":   {"actionlint_1.7.12_linux_arm64.tar.gz", "325e971b6ba9bfa504672e29be93c24981eeb1c07576d730e9f7c8805afff0c6"},
	}},
	"zizmor": {"1.32.0", "zizmor", "https://github.com/zizmorcore/zizmor/releases/download/v1.32.0/", map[string]rvAsset{
		"windows/amd64": {"zizmor-x86_64-pc-windows-msvc.zip", "d284a16e09d9df4ce2c000357e7b59bd531be093bea5718a1ce225b449b59d49"},
		"linux/amd64":   {"zizmor-x86_64-unknown-linux-gnu.tar.gz", "bb110cc765eca9cd7efb2e284a956be5974c60027b0970f16397d86208a9957b"},
		"linux/arm64":   {"zizmor-aarch64-unknown-linux-gnu.tar.gz", "cb152b207f815116d7fa1c1ee4614f61578803351395052908095e79adfd9fd0"},
	}},
	"detekt": {"1.23.8", "detekt-cli-1.23.8-all.jar", "https://github.com/detekt/detekt/releases/download/v1.23.8/", map[string]rvAsset{
		"any": {"detekt-cli-1.23.8-all.jar", "2ce2ff952e150baf28a29cda70a363b0340b3e81a55f43e51ec5edffc3d066c1"},
	}},
}

const rvRulesURL = "https://github.com/opengrep/opengrep-rules/archive/refs/heads/main.zip"

var rvInstallMu sync.Mutex

func rvToolsDir() string { return filepath.Join(checkerDir(), "tools") }

func rvDownload(u string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "codereview/1 (+"+runtime.GOOS+")")
	resp, err := (&http.Client{Timeout: 15 * time.Minute}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("download %s: HTTP %d", u, resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("download %s: bigger than %d MB", u, limit>>20)
	}
	return b, nil
}

// rvUnpack returns the file called want from a .zip or .tar.gz archive (or the data itself).
func rvUnpack(name string, data []byte, want string) ([]byte, error) {
	low := strings.ToLower(name)
	switch {
	case strings.HasSuffix(low, ".zip"):
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if strings.EqualFold(filepath.Base(f.Name), want) {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
		return nil, fmt.Errorf("%s not in %s", want, name)
	case strings.HasSuffix(low, ".tar.gz"), strings.HasSuffix(low, ".tgz"):
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				return nil, fmt.Errorf("%s not in %s", want, name)
			}
			if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == want {
				return io.ReadAll(tr)
			}
		}
	}
	return data, nil
}

// rvTool returns the path of a pinned review tool, downloading and verifying it on first use.
func rvTool(name string) (string, string, error) {
	spec, ok := rvTools[name]
	if !ok {
		return "", "", fmt.Errorf("unknown tool %s", name)
	}
	asset, ok := spec.assets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		if asset, ok = spec.assets["any"]; !ok {
			return "", "", fmt.Errorf("%s has no build for %s/%s", name, runtime.GOOS, runtime.GOARCH)
		}
	}
	exe := spec.exe
	if runtime.GOOS == "windows" && !strings.HasSuffix(exe, ".jar") {
		exe += ".exe"
	}
	dest := filepath.Join(rvToolsDir(), name+"-"+spec.version, exe)
	if ckExists(dest) {
		return dest, "", nil
	}
	rvInstallMu.Lock()
	defer rvInstallMu.Unlock()
	if ckExists(dest) {
		return dest, "", nil
	}
	data, err := rvDownload(spec.url+asset.file, 400<<20)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != asset.sha {
		return "", "", fmt.Errorf("%s download failed its checksum; not used", asset.file)
	}
	bin, err := rvUnpack(asset.file, data, exe)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", "", err
	}
	tmp := dest + ".part"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return "", "", err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return "", "", err
	}
	return dest, fmt.Sprintf("installed %s %s", name, spec.version), nil
}

var rvRuleLangs = map[string]bool{"python": true, "javascript": true, "typescript": true, "go": true, "java": true, "kotlin": true,
	"bash": true, "dockerfile": true, "c": true, "csharp": true, "rust": true, "php": true, "ruby": true, "swift": true, "scala": true}

// rvRules returns the folder of Opengrep's community rules (YAML only, for the languages above).
func rvRules(force bool) (string, string, error) {
	dir := filepath.Join(rvToolsDir(), "opengrep-rules")
	marker := filepath.Join(dir, ".ready")
	if !force && ckExists(marker) {
		return dir, "", nil
	}
	rvInstallMu.Lock()
	defer rvInstallMu.Unlock()
	if !force && ckExists(marker) {
		return dir, "", nil
	}
	data, err := rvDownload(rvRulesURL, 200<<20)
	if err != nil {
		return "", "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", err
	}
	tmp := dir + ".part"
	os.RemoveAll(tmp)
	n := 0
	for _, f := range zr.File {
		parts := strings.Split(f.Name, "/")
		if len(parts) < 3 || !rvRuleLangs[parts[1]] || f.FileInfo().IsDir() {
			continue
		}
		low := strings.ToLower(f.Name)
		if !(strings.HasSuffix(low, ".yaml") || strings.HasSuffix(low, ".yml")) || strings.Contains(low, ".test.y") || strings.Contains(low, ".fixed.") {
			continue
		}
		rel := filepath.Join(parts[1:]...)
		out := filepath.Join(tmp, rel)
		if !strings.HasPrefix(out, tmp+string(os.PathSeparator)) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		os.MkdirAll(filepath.Dir(out), 0o755)
		if os.WriteFile(out, b, 0o644) == nil {
			n++
		}
	}
	if n < 100 {
		os.RemoveAll(tmp)
		return "", "", fmt.Errorf("the rules download looked wrong (%d rule files)", n)
	}
	os.WriteFile(filepath.Join(tmp, ".ready"), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", "", err
	}
	return dir, fmt.Sprintf("installed %d Opengrep rules", n), nil
}

// rvVenv makes sure the checker's Python env exists (for lizard and ShellCheck); it does not
// need Node like the full check setup does.
func rvVenv() error {
	py := ckVenvBin("python")
	if ckExists(py) {
		return nil
	}
	venv := filepath.Join(checkerDir(), "py")
	os.MkdirAll(checkerDir(), 0o755)
	if uv, err := exec.LookPath("uv"); err == nil {
		_, err := ckStep("creating Python env", checkerDir(), 5*time.Minute, uv, "venv", "--allow-existing", venv)
		return err
	}
	sys := "python3"
	if runtime.GOOS == "windows" {
		sys = "python"
	}
	pyExe, err := exec.LookPath(sys)
	if err != nil {
		return errors.New("no Python found")
	}
	_, err = ckStep("creating Python env", checkerDir(), 5*time.Minute, pyExe, "-m", "venv", venv)
	return err
}

func rvPip(pkgs ...string) error {
	if err := rvVenv(); err != nil {
		return err
	}
	py := ckVenvBin("python")
	if uv, err := exec.LookPath("uv"); err == nil {
		_, err := ckStep("installing "+strings.Join(pkgs, ", "), checkerDir(), 10*time.Minute, uv, append([]string{"pip", "install", "--python", py}, pkgs...)...)
		return err
	}
	_, err := ckStep("installing "+strings.Join(pkgs, ", "), checkerDir(), 10*time.Minute, py, append([]string{"-m", "pip", "install", "--quiet", "--disable-pip-version-check"}, pkgs...)...)
	return err
}

var rvComplexLangs = []string{"py", "js", "ts", "go", "kt", "java", "c", "cs", "swift", "rs", "php", "rb", "lua", "scala"}

// rvPrepare does the installs that can't run in parallel (the Python env and pip) and decides
// what the layers can rely on.
func rvPrepare(c *rvCtx) []string {
	var msgs []string
	if !rvSkipped(c.a.Skip, "lint") && len(c.rvFiles("py", "js", "ts")) > 0 {
		if m, err := ckSetup(false); err != nil {
			c.setupErr = err.Error()
		} else if m != "" {
			msgs = append(msgs, strings.SplitN(m, "\n", 2)[0])
		}
	}
	c.lintPy = c.setupErr == "" && !rvSkipped(c.a.Skip, "lint") && len(c.rvFiles("py")) > 0 && ckExists(ckVenvBin("ruff"))
	var pkgs []string
	if !rvSkipped(c.a.Skip, "complexity") && len(c.rvFiles(rvComplexLangs...)) > 0 && !ckExists(ckVenvBin("lizard")) {
		pkgs = append(pkgs, "lizard")
	}
	if !rvSkipped(c.a.Skip, "scripts") && len(c.rvFiles("sh"))+len(c.inv.wf) > 0 && !ckExists(ckVenvBin("shellcheck")) {
		pkgs = append(pkgs, "shellcheck-py")
	}
	if len(pkgs) > 0 {
		if err := rvPip(pkgs...); err != nil {
			msgs = append(msgs, "couldn't install "+strings.Join(pkgs, ", ")+": "+err.Error())
		} else {
			msgs = append(msgs, "installed "+strings.Join(pkgs, ", "))
		}
	}
	return msgs
}

// rvSetup installs every review tool this machine can use (check action:setup).
func rvSetup() string {
	var done, failed []string
	names := make([]string, 0, len(rvTools))
	for n := range rvTools {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if n == "detekt" && rvJava() == "" {
			continue
		}
		if _, m, err := rvTool(n); err != nil {
			failed = append(failed, n+": "+err.Error())
		} else if m != "" {
			done = append(done, m)
		}
	}
	if _, m, err := rvRules(true); err != nil {
		failed = append(failed, "opengrep rules: "+err.Error())
	} else if m != "" {
		done = append(done, m)
	}
	if err := rvPip("lizard", "shellcheck-py"); err != nil {
		failed = append(failed, "lizard/shellcheck: "+err.Error())
	}
	if runtime.GOOS == "windows" {
		if _, m, err := rvPSSA(); err != nil {
			failed = append(failed, "PSScriptAnalyzer: "+err.Error())
		} else if m != "" {
			done = append(done, m)
		}
	}
	s := "Review tools ready in " + rvToolsDir()
	if len(done) > 0 {
		s += " (" + strings.Join(done, ", ") + ")"
	}
	for _, f := range failed {
		s += "\n⚠ " + f
	}
	return s
}

func rvTmp(prefix, ext string) string {
	dir := filepath.Join(checkerDir(), "work")
	os.MkdirAll(dir, 0o755)
	return filepath.Join(dir, fmt.Sprintf("%s-%d-%s%s", prefix, time.Now().UnixNano(), randHexSafe(), ext))
}

var rvTmpN struct {
	sync.Mutex
	n int
}

func randHexSafe() string {
	rvTmpN.Lock()
	defer rvTmpN.Unlock()
	rvTmpN.n++
	return strconv.Itoa(rvTmpN.n)
}

func rvFromCk(in []ckIssue, scope int) []rvIssue {
	out := make([]rvIssue, 0, len(in))
	for _, is := range in {
		sev := rvErr
		if is.Warn {
			sev = rvWarn
		}
		out = append(out, rvIssue{Tool: is.Tool, File: is.File, Line: is.Line, Col: is.Col, Sev: sev, Code: is.Code, Msg: is.Msg, scope: scope})
	}
	return out
}

func rvAbsAll(c *rvCtx, rels []string) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = c.abs(r)
	}
	return out
}

func rvFirstSentence(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if i := strings.Index(s, ". "); i > 20 && i < n {
		s = s[:i+1]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// ---- lint: the check linters, deeper settings for Ruff, golangci-lint for Go ----

const rvRuffSelect = "E4,E7,E9,F,B,S,BLE,ASYNC,PLE,PIE,T10"
const rvRuffIgnore = "S101,S311,S603,S607,S404,B008,PIE790"

var rvRuffSyntax = regexp.MustCompile(`^(.+?):(\d+):(\d+): SyntaxError: (.*)$`)

func rvRuffSev(code string) (int, int) {
	switch {
	case code == "E999" || strings.HasPrefix(code, "E9"), strings.HasPrefix(code, "F82"), strings.HasPrefix(code, "F63"),
		strings.HasPrefix(code, "F70"), code == "F502", code == "F503", code == "F506", code == "F507", code == "F508", code == "F509":
		return rvErr, rvScopeFile
	case strings.HasPrefix(code, "PLE"), code == "T100":
		return rvErr, rvScopeLine
	}
	return rvWarn, rvScopeLine
}

func rvRuff(c *rvCtx, files []string) rvResult {
	var r rvResult
	args := []string{"check", "--output-format", "concise", "--no-cache", "--exit-zero", "--force-exclude"}
	if ckExists(filepath.Join(c.root, "ruff.toml")) || ckExists(filepath.Join(c.root, ".ruff.toml")) || ckFileHas(filepath.Join(c.root, "pyproject.toml"), "[tool.ruff") {
		args = append(args, "--extend-select", "S,BLE,T10,ASYNC", "--extend-ignore", rvRuffIgnore)
	} else {
		args = append(args, "--select", rvRuffSelect, "--ignore", rvRuffIgnore)
	}
	if c.diff != nil {
		args = append(args, rvAbsAll(c, files)...)
	} else {
		args = append(args, ckTarg(c.root, c.single))
	}
	so, se, _, err := ckRun(c.root, 5*time.Minute, ckVenvBin("ruff"), args...)
	if err != nil {
		r.fails = append(r.fails, "ruff: "+err.Error())
		return r
	}
	r.ran = append(r.ran, "ruff")
	for _, l := range strings.Split(so+"\n"+se, "\n") {
		l = strings.TrimRight(l, "\r")
		if m := rvRuffSyntax.FindStringSubmatch(l); m != nil {
			ln, _ := strconv.Atoi(m[2])
			col, _ := strconv.Atoi(m[3])
			r.add(rvIssue{Tool: "ruff", File: ckRel(c.root, m[1]), Line: ln, Col: col, Sev: rvErr, Code: "syntax", Msg: m[4], scope: rvScopeFile})
			continue
		}
		m := ckRuffLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		col, _ := strconv.Atoi(m[3])
		sev, scope := rvRuffSev(m[4])
		r.add(rvIssue{Tool: "ruff", File: ckRel(c.root, m[1]), Line: ln, Col: col, Sev: sev, Code: m[4], Msg: m[5], scope: scope})
	}
	return r
}

func rvLint(c *rvCtx) rvResult {
	var r rvResult
	py, jsts := c.rvFiles("py"), c.rvFiles("js", "ts")
	if c.setupErr != "" && len(py)+len(jsts) > 0 {
		r.fails = append(r.fails, "Python/JS linters: "+c.setupErr)
		py, jsts = nil, nil
	}
	merge := func(x rvResult) {
		r.ran, r.fails, r.notes = append(r.ran, x.ran...), append(r.fails, x.fails...), append(r.notes, x.notes...)
		r.issues = append(r.issues, x.issues...)
	}
	if len(py) > 0 {
		merge(rvRuff(c, py))
		is, fail := ckMypy(c.root, c.single, c.ign)
		r.add(rvFromCk(is, rvScopeFile)...)
		if fail != "" {
			r.fails = append(r.fails, fail)
		}
		r.ran = append(r.ran, "mypy")
	}
	if len(jsts) > 0 {
		is, fail, note := ckEslint(c.root, c.single, false, c.ign)
		r.add(rvFromCk(is, rvScopeLine)...)
		r.ran = append(r.ran, "eslint")
		if fail != "" {
			r.fails = append(r.fails, fail)
		}
		if note != "" {
			r.notes = append(r.notes, note)
		}
		if len(c.inv.byLang["ts"]) > 0 || ckExists(filepath.Join(c.root, "tsconfig.json")) {
			is, fail, note := ckTsc(c.root, c.single, len(c.inv.byLang["ts"]), c.ign)
			r.add(rvFromCk(is, rvScopeFile)...)
			r.ran = append(r.ran, "tsc")
			if fail != "" {
				r.fails = append(r.fails, fail)
			}
			if note != "" {
				r.notes = append(r.notes, note)
			}
		}
	}
	if c.single == "" {
		for _, mod := range rvGoModules(c) {
			merge(rvGolangci(c, mod))
		}
	}
	if c.inv.droid && c.single == "" && (c.diff == nil || len(c.rvFiles("kt", "java")) > 0 || rvChangedAny(c, ".xml", ".gradle", ".kts")) {
		is, fail, note := ckAndroid(c.root)
		r.add(rvFromCk(is, rvScopeLine)...)
		r.ran = append(r.ran, "android lint")
		if fail != "" {
			if hint := rvEnvHint(fail); hint != "" {
				fail = "android lint couldn't run: " + hint
			}
			r.fails = append(r.fails, fail)
		}
		if note != "" {
			r.notes = append(r.notes, note)
		}
	}
	return r
}

func rvChangedAny(c *rvCtx, exts ...string) bool {
	if c.diff == nil {
		return true
	}
	for _, k := range c.diff.Order {
		low := strings.ToLower(c.diff.Files[k].Path)
		for _, e := range exts {
			if strings.HasSuffix(low, e) {
				return true
			}
		}
	}
	return false
}

const rvGolangciConfig = `version: "2"
linters:
  default: standard
  enable: [gosec, bodyclose, errorlint, nilerr, noctx, durationcheck, errchkjson, makezero, sqlclosecheck, rowserrcheck, wastedassign, forcetypeassert, contextcheck, predeclared, reassign, musttag, nolintlint, testifylint]
  settings:
    gosec:
      excludes: [G104, G115, G301, G302, G306, G307]
  exclusions:
    generated: lax
    presets: [std-error-handling]
`

var rvGoCode = regexp.MustCompile(`^((?:G|SA|S|ST|QF)\d{3,4}):\s*`)

func rvGoSev(linter, code, sev string) int {
	switch linter {
	case "typecheck", "govet", "sqlclosecheck", "rowserrcheck", "durationcheck":
		return rvErr
	case "gosec":
		if code == "G404" || code == "G101" || code == "G703" || code == "G304" { // math/rand is fine outside crypto, secrets are gitleaks' job, reading a path the user gave is what CLIs do
			return rvWarn
		}
		switch strings.ToLower(sev) {
		case "high":
			return rvErr
		case "low":
			return rvNote
		}
		return rvWarn
	case "staticcheck":
		if strings.HasPrefix(code, "SA") && code != "SA1019" {
			return rvErr
		}
		if strings.HasPrefix(code, "SA") {
			return rvWarn
		}
		return rvNote
	case "misspell":
		return rvNote
	}
	return rvWarn
}

// rvGoModules lists the Go modules (folders with a go.mod) that have Go files to review.
func rvGoModules(c *rvCtx) []string {
	var mods []string
	for _, f := range c.inv.files {
		if filepath.Base(f) == "go.mod" && !strings.Contains("/"+f, "/testdata/") {
			mods = append(mods, filepath.ToSlash(filepath.Dir(f)))
		}
	}
	sort.Slice(mods, func(i, j int) bool { return len(mods[i]) > len(mods[j]) })
	owner := func(f string) string {
		for _, m := range mods {
			if m == "." || strings.HasPrefix(f, m+"/") {
				return m
			}
		}
		return ""
	}
	want := map[string]bool{}
	for _, f := range c.rvFiles("go") {
		if m := owner(f); m != "" {
			want[m] = true
		}
	}
	var out []string
	for _, m := range mods {
		if want[m] && len(out) < 8 {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

func rvGolangci(c *rvCtx, mod string) rvResult {
	var r rvResult
	dir := c.abs(mod)
	bin, msg, err := rvTool("golangci-lint")
	if msg != "" {
		r.notes = append(r.notes, msg)
	}
	fallback := func(why string) rvResult {
		is, fail, note := ckGo(dir)
		for i := range is {
			is[i].File = rvRel(c.root, filepath.Join(dir, filepath.FromSlash(is[i].File)))
		}
		r.add(rvFromCk(is, rvScopeLine)...)
		r.ran = append(r.ran, "vet", "staticcheck")
		if fail != "" {
			r.fails = append(r.fails, fail)
		}
		if note != "" {
			r.notes = append(r.notes, note)
		}
		r.notes = append(r.notes, "golangci-lint not used ("+why+"): ran go vet + staticcheck instead")
		return r
	}
	if err != nil {
		return fallback(err.Error())
	}
	own := filepath.Join(checkerDir(), "review-golangci.yml")
	os.WriteFile(own, []byte(rvGolangciConfig), 0o644)
	hasCfg := false
	for _, n := range []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"} {
		if ckExists(filepath.Join(dir, n)) {
			hasCfg = true
		}
	}
	run := func(cfg []string) ([]byte, string, error) {
		out := rvTmp("golangci", ".json")
		defer os.Remove(out)
		args := append([]string{"run", "--output.json.path=" + out, "--output.text.path=stderr", "--issues-exit-code=0", "--show-stats=false", "--path-mode=abs", "--timeout=8m"}, cfg...)
		args = append(args, "./...")
		so, se, code, err := ckRun(dir, 10*time.Minute, bin, args...)
		if err != nil {
			return nil, "", err
		}
		b, rerr := os.ReadFile(out)
		if rerr != nil || code != 0 {
			return nil, ckFirstLines(se+"\n"+so, 4), errors.New("failed")
		}
		return b, "", nil
	}
	var cfg []string
	if !hasCfg {
		cfg = []string{"--config", own}
	}
	b, why, err := run(cfg)
	if err != nil && hasCfg {
		if b, _, err = run([]string{"--config", own}); err == nil {
			r.notes = append(r.notes, "the project's golangci-lint config didn't load ("+why+"); used review defaults")
		}
	}
	if err != nil {
		if why == "" {
			why = err.Error()
		}
		return fallback(why)
	}
	var res struct {
		Issues []struct {
			FromLinter string `json:"FromLinter"`
			Text       string `json:"Text"`
			Severity   string `json:"Severity"`
			Pos        struct {
				Filename string `json:"Filename"`
				Line     int    `json:"Line"`
				Column   int    `json:"Column"`
			} `json:"Pos"`
		} `json:"Issues"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		return fallback("unreadable output")
	}
	r.ran = append(r.ran, "golangci-lint")
	for _, is := range res.Issues {
		code, text := "", is.Text
		if m := rvGoCode.FindStringSubmatch(text); m != nil {
			code, text = m[1], text[len(m[0]):]
		}
		sev := rvGoSev(is.FromLinter, code, is.Severity)
		scope := rvScopeLine
		if is.FromLinter == "typecheck" {
			scope = rvScopeFile
		}
		p := is.Pos.Filename
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, filepath.FromSlash(p))
		}
		r.add(rvIssue{Tool: is.FromLinter, File: rvRel(c.root, p), Line: is.Pos.Line, Col: is.Pos.Column, Sev: sev, Code: code, Msg: text, scope: scope})
	}
	return r
}

// ---- security: Opengrep community rules ----

var rvGrepDirs = map[string][]string{"py": {"python"}, "js": {"javascript"}, "ts": {"typescript", "javascript"}, "go": {"go"},
	"kt": {"kotlin"}, "java": {"java"}, "sh": {"bash"}, "c": {"c"}, "cs": {"csharp"}, "rs": {"rust"}, "php": {"php"},
	"rb": {"ruby"}, "swift": {"swift"}, "scala": {"scala"}}

// Rules that are too noisy at their own level for everyday apps.
var rvGrepOverride = map[string]int{"incorrect-default-permission": -1, "use-tls": rvNote, "missing-ssl-minversion": rvNote,
	"math-random-used": rvWarn, "use-raise-for-status": -1, "express-check-csurf-middleware-usage": -1}

func rvGrepSev(cat string, sub []string, conf, sev string) int {
	cat, conf, sev = strings.ToLower(cat), strings.ToUpper(conf), strings.ToUpper(sev)
	vuln := false
	for _, s := range sub {
		if s = strings.ToLower(s); s == "vuln" || s == "secure default" {
			vuln = true
		}
	}
	switch cat {
	case "security":
		if (vuln && (conf == "MEDIUM" || conf == "HIGH")) || (sev == "ERROR" && conf == "HIGH") {
			return rvErr
		}
		if sev == "INFO" && conf == "LOW" {
			return -1
		}
		return rvWarn
	case "correctness":
		return rvWarn
	}
	if sev == "INFO" {
		return -1
	}
	return rvNote
}

func rvSecurity(c *rvCtx) rvResult {
	var r rvResult
	var langs []string
	for l := range rvGrepDirs {
		langs = append(langs, l)
	}
	var files []string
	dirs := map[string]bool{}
	for _, f := range c.rvFiles(langs...) {
		if rvIsTest(f) {
			continue
		}
		files = append(files, f)
		for _, d := range rvGrepDirs[rvLang(f)] {
			dirs[d] = true
		}
	}
	for _, f := range c.inv.files {
		if b := strings.ToLower(filepath.Base(f)); (b == "dockerfile" || strings.HasSuffix(b, ".dockerfile")) && c.changed(f) {
			files = append(files, f)
			dirs["dockerfile"] = true
		}
	}
	if len(files) == 0 {
		return r
	}
	bin, msg, err := rvTool("opengrep")
	if msg != "" {
		r.notes = append(r.notes, msg)
	}
	if err != nil {
		r.fails = append(r.fails, "opengrep: "+err.Error())
		return r
	}
	rules, msg, err := rvRules(false)
	if msg != "" {
		r.notes = append(r.notes, msg)
	}
	if err != nil {
		r.fails = append(r.fails, "opengrep rules: "+err.Error())
		return r
	}
	args := []string{"scan", "--json", "--quiet", "--disable-version-check", "-j", strconv.Itoa(max(1, runtime.NumCPU()/2)),
		"--timeout", "10", "--max-target-bytes", "1000000", "--exclude-minified-files"}
	var names []string
	for d := range dirs {
		if ckExists(filepath.Join(rules, d)) {
			names = append(names, d)
		}
	}
	if len(names) == 0 {
		return r
	}
	sort.Strings(names)
	for _, d := range names {
		args = append(args, "--config", filepath.Join(rules, d))
	}
	for d := range ckSkipDirs {
		args = append(args, "--exclude", d)
	}
	for d := range rvExtraSkip {
		args = append(args, "--exclude", d)
	}
	if c.diff != nil && len(files) <= 150 {
		args = append(args, rvAbsAll(c, files)...)
	} else if c.single != "" {
		args = append(args, c.single)
	} else {
		args = append(args, c.root)
	}
	so, se, _, err := ckRun(c.root, 10*time.Minute, bin, args...)
	if err != nil {
		r.fails = append(r.fails, "opengrep: "+err.Error())
		return r
	}
	var res struct {
		Results []struct {
			CheckID string `json:"check_id"`
			Path    string `json:"path"`
			Start   struct {
				Line int `json:"line"`
				Col  int `json:"col"`
			} `json:"start"`
			Extra struct {
				Message  string `json:"message"`
				Severity string `json:"severity"`
				Metadata struct {
					Category    string          `json:"category"`
					Subcategory json.RawMessage `json:"subcategory"`
					Confidence  string          `json:"confidence"`
				} `json:"metadata"`
			} `json:"extra"`
		} `json:"results"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal([]byte(so), &res); err != nil {
		r.fails = append(r.fails, "opengrep: "+ckFirstLines(se+"\n"+so, 3))
		return r
	}
	r.ran = append(r.ran, "opengrep ("+strings.Join(names, ", ")+")")
	for _, x := range res.Results {
		rel := rvRel(c.root, x.Path)
		if rvIsTest(rel) {
			continue
		}
		var sub []string
		if json.Unmarshal(x.Extra.Metadata.Subcategory, &sub) != nil {
			var one string
			if json.Unmarshal(x.Extra.Metadata.Subcategory, &one) == nil {
				sub = []string{one}
			}
		}
		sev := rvGrepSev(x.Extra.Metadata.Category, sub, x.Extra.Metadata.Confidence, x.Extra.Severity)
		id := x.CheckID
		if i := strings.LastIndex(id, "."); i >= 0 {
			id = id[i+1:]
		}
		if o, ok := rvGrepOverride[id]; ok {
			sev = o
		}
		if sev < 0 {
			continue
		}
		r.add(rvIssue{Tool: "opengrep", File: rel, Line: x.Start.Line, Col: x.Start.Col, Sev: sev, Code: id, Msg: rvFirstSentence(x.Extra.Message, 180)})
	}
	if len(res.Errors) > 0 {
		r.notes = append(r.notes, fmt.Sprintf("opengrep: %d file(s) or rule(s) couldn't be analysed fully", len(res.Errors)))
	}
	return r
}

// ---- secrets: gitleaks ----

func rvPathSkipped(rel string, ign []string) bool {
	if ckIgnored(rel, ign) {
		return true
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts[:len(parts)-1] {
		if ckSkipDirs[p] || rvExtraSkip[p] || ckIgnored(strings.Join(parts[:i+1], "/")+"/", ign) {
			return true
		}
	}
	return false
}

func rvSecrets(c *rvCtx) rvResult {
	var r rvResult
	bin, msg, err := rvTool("gitleaks")
	if msg != "" {
		r.notes = append(r.notes, msg)
	}
	if err != nil {
		r.fails = append(r.fails, "gitleaks: "+err.Error())
		return r
	}
	target := c.root
	if c.single != "" {
		target = c.single
	}
	out := rvTmp("gitleaks", ".json")
	defer os.Remove(out)
	_, se, code, err := ckRun(c.root, 10*time.Minute, bin, "dir", target, "--report-format", "json", "--report-path", out,
		"--no-banner", "--exit-code", "0", "--redact", "--max-target-megabytes", "5", "--log-level", "error")
	if err != nil {
		r.fails = append(r.fails, "gitleaks: "+err.Error())
		return r
	}
	b, rerr := os.ReadFile(out)
	if rerr != nil {
		r.fails = append(r.fails, fmt.Sprintf("gitleaks (exit %d): %s", code, ckFirstLines(se, 3)))
		return r
	}
	var res []struct {
		RuleID      string `json:"RuleID"`
		File        string `json:"File"`
		Description string `json:"Description"`
		StartLine   int    `json:"StartLine"`
	}
	json.Unmarshal(b, &res)
	r.ran = append(r.ran, "gitleaks")
	for _, x := range res {
		rel := rvRel(c.root, x.File)
		if rvPathSkipped(rel, c.ign) {
			continue
		}
		sev := rvErr
		if rvIsTest(rel) {
			sev = rvWarn
		}
		r.add(rvIssue{Tool: "gitleaks", File: rel, Line: x.StartLine, Sev: sev, Code: x.RuleID,
			Msg: rvFirstSentence(x.Description, 120) + " Remove it from the code (and from git history) and replace the key."})
	}
	return r
}

// ---- CI: actionlint + zizmor on GitHub Actions workflows ----

func rvCI(c *rvCtx) rvResult {
	var r rvResult
	var wf []string
	for _, f := range c.inv.wf {
		if c.changed(f) {
			wf = append(wf, f)
		}
	}
	if len(wf) == 0 {
		return r
	}
	abs := rvAbsAll(c, wf)
	if bin, msg, err := rvTool("actionlint"); err != nil {
		r.fails = append(r.fails, "actionlint: "+err.Error())
	} else {
		if msg != "" {
			r.notes = append(r.notes, msg)
		}
		sc := ""
		if p := ckVenvBin("shellcheck"); ckExists(p) {
			sc = p
		}
		so, se, _, err := ckRun(c.root, 5*time.Minute, bin, append([]string{"-format", "{{json .}}", "-shellcheck=" + sc, "-pyflakes="}, abs...)...)
		var res []struct {
			Message  string `json:"message"`
			Filepath string `json:"filepath"`
			Kind     string `json:"kind"`
			Line     int    `json:"line"`
			Column   int    `json:"column"`
		}
		if err == nil && (strings.TrimSpace(so) == "" || strings.TrimSpace(so) == "null" || json.Unmarshal([]byte(so), &res) == nil) {
			r.ran = append(r.ran, "actionlint")
			for _, x := range res {
				sev := rvErr
				if x.Kind == "shellcheck" {
					switch {
					case strings.Contains(x.Message, ":error:"):
					case strings.Contains(x.Message, ":warning:"):
						sev = rvWarn
					default:
						continue
					}
				}
				r.add(rvIssue{Tool: "actionlint", File: rvRel(c.root, x.Filepath), Line: x.Line, Col: x.Column, Sev: sev, Code: x.Kind, Msg: rvFirstSentence(x.Message, 200)})
			}
		} else {
			r.fails = append(r.fails, "actionlint: "+ckFirstLines(se+"\n"+so, 3))
		}
	}
	if bin, msg, err := rvTool("zizmor"); err != nil {
		r.fails = append(r.fails, "zizmor: "+err.Error())
	} else {
		if msg != "" {
			r.notes = append(r.notes, msg)
		}
		so, se, _, err := ckRun(c.root, 5*time.Minute, bin, append([]string{"--offline", "--no-exit-codes", "--format", "json", "--no-progress"}, abs...)...)
		var res []struct {
			Ident          string `json:"ident"`
			Desc           string `json:"desc"`
			Determinations struct {
				Confidence string `json:"confidence"`
				Severity   string `json:"severity"`
			} `json:"determinations"`
			Locations []struct {
				Symbolic struct {
					Key struct {
						Local struct {
							GivenPath    string `json:"given_path"`
							VerbatimPath string `json:"verbatim_path"`
						} `json:"Local"`
					} `json:"key"`
					Annotation string `json:"annotation"`
					Kind       string `json:"kind"`
				} `json:"symbolic"`
				Concrete struct {
					Location struct {
						StartPoint struct {
							Row    int `json:"row"`
							Column int `json:"column"`
						} `json:"start_point"`
					} `json:"location"`
				} `json:"concrete"`
			} `json:"locations"`
		}
		if err == nil && json.Unmarshal([]byte(so), &res) == nil {
			r.ran = append(r.ran, "zizmor")
			hygiene := map[string]bool{"unpinned-uses": true, "artipacked": true, "excessive-permissions": true, "unpinned-images": true, "undocumented-permissions": true, "anonymous-definition": true}
			pedantic := map[string]bool{"unpinned-uses": true, "artipacked": true, "unpinned-images": true, "undocumented-permissions": true, "anonymous-definition": true}
			for _, x := range res {
				sevS, conf := strings.ToLower(x.Determinations.Severity), strings.ToLower(x.Determinations.Confidence)
				sev := rvWarn
				switch {
				case pedantic[x.Ident]:
					sev = rvNote
				case sevS == "high" && conf != "low" && !hygiene[x.Ident]:
					sev = rvErr
				case sevS == "informational" || sevS == "unknown" || sevS == "low":
					continue
				}
				if len(x.Locations) == 0 {
					continue
				}
				loc := x.Locations[0]
				for _, l := range x.Locations {
					if l.Symbolic.Kind == "Primary" {
						loc = l
						break
					}
				}
				p := loc.Symbolic.Key.Local.GivenPath
				if p == "" {
					p = loc.Symbolic.Key.Local.VerbatimPath
				}
				m := x.Desc
				if loc.Symbolic.Annotation != "" {
					m += ": " + loc.Symbolic.Annotation
				}
				r.add(rvIssue{Tool: "zizmor", File: rvRel(c.root, p), Line: loc.Concrete.Location.StartPoint.Row + 1, Sev: sev, Code: x.Ident, Msg: m})
			}
		} else {
			r.fails = append(r.fails, "zizmor: "+ckFirstLines(se+"\n"+so, 3))
		}
	}
	return r
}

// ---- scripts: ShellCheck and PSScriptAnalyzer ----

func rvPSSA() (string, string, error) {
	dir := filepath.Join(rvToolsDir(), "psscriptanalyzer")
	psd := filepath.Join(dir, "PSScriptAnalyzer.psd1")
	if ckExists(psd) {
		return psd, "", nil
	}
	rvInstallMu.Lock()
	defer rvInstallMu.Unlock()
	if ckExists(psd) {
		return psd, "", nil
	}
	data, err := rvDownload("https://www.powershellgallery.com/api/v2/package/PSScriptAnalyzer", 100<<20)
	if err != nil {
		return "", "", err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", "", err
	}
	tmp := dir + ".part"
	os.RemoveAll(tmp)
	for _, f := range zr.File {
		n := f.Name
		if f.FileInfo().IsDir() || strings.HasPrefix(n, "_rels/") || strings.HasPrefix(n, "package/") || n == "[Content_Types].xml" || strings.HasSuffix(n, ".nuspec") {
			continue
		}
		n, _ = url_unescape(n)
		out := filepath.Join(tmp, filepath.FromSlash(n))
		if !strings.HasPrefix(out, tmp+string(os.PathSeparator)) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		os.MkdirAll(filepath.Dir(out), 0o755)
		os.WriteFile(out, b, 0o644)
	}
	if !ckExists(filepath.Join(tmp, "PSScriptAnalyzer.psd1")) {
		os.RemoveAll(tmp)
		return "", "", errors.New("PSScriptAnalyzer.psd1 not in the package")
	}
	os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return "", "", err
	}
	return psd, "installed PSScriptAnalyzer", nil
}

// url_unescape undoes the %20-style escapes NuGet packages use in entry names.
func url_unescape(s string) (string, error) {
	if !strings.Contains(s, "%") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String(), nil
}

const rvPSSAScript = `param([string]$List, [string]$Module)
$ErrorActionPreference = 'Stop'
Import-Module $Module
$ex = 'PSAvoidUsingWriteHost','PSUseShouldProcessForStateChangingFunctions','PSUseSingularNouns','PSUseBOMForUnicodeEncodedFile','PSAvoidTrailingWhitespace','PSUseDeclaredVarsMoreThanAssignments'
$r = foreach ($f in Get-Content -LiteralPath $List) { Invoke-ScriptAnalyzer -Path $f -Severity Error,Warning -ExcludeRule $ex }
@($r | ForEach-Object { [pscustomobject]@{ Rule = $_.RuleName; Severity = "$($_.Severity)"; File = $_.ScriptPath; Line = $_.Line; Column = $_.Column; Message = $_.Message } }) | ConvertTo-Json -Depth 3 -Compress
`

func rvScripts(c *rvCtx) rvResult {
	var r rvResult
	if sh := c.rvFiles("sh"); len(sh) > 0 {
		if p := ckVenvBin("shellcheck"); !ckExists(p) {
			r.fails = append(r.fails, "shellcheck: not installed (needs Python)")
		} else {
			so, se, _, err := ckRun(c.root, 5*time.Minute, p, append([]string{"-f", "json1", "-S", "warning", "-x"}, rvAbsAll(c, sh)...)...)
			var res struct {
				Comments []struct {
					File    string `json:"file"`
					Level   string `json:"level"`
					Message string `json:"message"`
					Line    int    `json:"line"`
					Column  int    `json:"column"`
					Code    int    `json:"code"`
				} `json:"comments"`
			}
			if err == nil && json.Unmarshal([]byte(so), &res) == nil {
				r.ran = append(r.ran, "shellcheck")
				crlf := map[string]bool{}
				for _, x := range res.Comments {
					rel := rvRel(c.root, x.File)
					if x.Code == 1017 { // carriage returns: only a problem when git stores them
						if !crlf[rel] {
							crlf[rel] = true
							if eol, err := rvGit(c.root, "ls-files", "--eol", "--", rel); err == nil && strings.Contains(eol, "i/crlf") {
								r.add(rvIssue{Tool: "shellcheck", File: rel, Line: 1, Sev: rvErr, Code: "SC1017",
									Msg: "committed with Windows line endings (CRLF): bash fails on them. Convert to LF and add \"*.sh text eol=lf\" to .gitattributes"})
							}
						}
						continue
					}
					sev := rvWarn
					if x.Level == "error" {
						sev = rvErr
					}
					r.add(rvIssue{Tool: "shellcheck", File: rel, Line: x.Line, Col: x.Column, Sev: sev, Code: "SC" + strconv.Itoa(x.Code), Msg: x.Message})
				}
			} else {
				r.fails = append(r.fails, "shellcheck: "+ckFirstLines(se+"\n"+so, 3))
			}
		}
	}
	ps := c.rvFiles("ps1")
	if len(ps) == 0 {
		return r
	}
	shell := ""
	if runtime.GOOS == "windows" {
		shell = "powershell.exe"
	} else if p, err := exec.LookPath("pwsh"); err == nil {
		shell = p
	}
	if shell == "" {
		r.notes = append(r.notes, "PowerShell scripts not analysed (no pwsh on this machine)")
		return r
	}
	psd, msg, err := rvPSSA()
	if msg != "" {
		r.notes = append(r.notes, msg)
	}
	if err != nil {
		r.fails = append(r.fails, "PSScriptAnalyzer: "+err.Error())
		return r
	}
	script := filepath.Join(checkerDir(), "review-pssa.ps1")
	os.WriteFile(script, []byte(rvPSSAScript), 0o644)
	list := rvTmp("pssa", ".txt")
	defer os.Remove(list)
	os.WriteFile(list, []byte(strings.Join(rvAbsAll(c, ps), "\n")), 0o644)
	so, se, code, err := ckRun(c.root, 5*time.Minute, shell, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, "-List", list, "-Module", psd)
	if err != nil || code != 0 {
		r.fails = append(r.fails, "PSScriptAnalyzer: "+ckFirstLines(se+"\n"+so, 3))
		return r
	}
	var res []struct {
		Rule     string `json:"Rule"`
		Severity string `json:"Severity"`
		File     string `json:"File"`
		Message  string `json:"Message"`
		Line     int    `json:"Line"`
		Column   int    `json:"Column"`
	}
	s := strings.TrimSpace(so)
	if strings.HasPrefix(s, "{") {
		s = "[" + s + "]"
	}
	if s != "" && json.Unmarshal([]byte(s), &res) != nil {
		r.fails = append(r.fails, "PSScriptAnalyzer: unreadable output")
		return r
	}
	r.ran = append(r.ran, "PSScriptAnalyzer")
	for _, x := range res {
		sev := rvWarn
		if x.Severity == "Error" || x.Severity == "ParseError" {
			sev = rvErr
		}
		r.add(rvIssue{Tool: "pssa", File: rvRel(c.root, x.File), Line: x.Line, Col: x.Column, Sev: sev, Code: x.Rule, Msg: x.Message})
	}
	return r
}

// ---- kotlin: detekt ----

func rvJava() string {
	exe := "java"
	if runtime.GOOS == "windows" {
		exe = "java.exe"
	}
	if h := os.Getenv("JAVA_HOME"); h != "" && ckExists(filepath.Join(h, "bin", exe)) {
		return filepath.Join(h, "bin", exe)
	}
	if p, err := exec.LookPath("java"); err == nil {
		return p
	}
	var cands []string
	if runtime.GOOS == "windows" {
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("LOCALAPPDATA") + `\Programs`} {
			cands = append(cands, filepath.Join(base, "Android", "Android Studio", "jbr", "bin", exe))
		}
	} else {
		home, _ := os.UserHomeDir()
		cands = append(cands, "/opt/android-studio/jbr/bin/java", filepath.Join(home, "android-studio", "jbr", "bin", "java"), "/snap/android-studio/current/jbr/bin/java")
	}
	for _, p := range cands {
		if ckExists(p) {
			return p
		}
	}
	return ""
}

const rvDetektConfig = `style:
  active: false
naming:
  active: false
comments:
  active: false
complexity:
  active: true
  TooManyFunctions:
    active: false
  LongParameterList:
    active: true
  LongMethod:
    threshold: 120
  CyclomaticComplexMethod:
    threshold: 25
potential-bugs:
  active: true
  UnsafeCallOnNullableType:
    active: true
  CastNullableToNonNullableType:
    active: true
exceptions:
  active: true
coroutines:
  active: true
  GlobalCoroutineUsage:
    active: true
performance:
  active: true
empty-blocks:
  active: true
`

// rvSarif reads the results of a SARIF report.
type rvSarifResult struct {
	Rule, Level, Msg, URI string
	Line, Col             int
}

func rvSarif(b []byte) ([]rvSarifResult, error) {
	var s struct {
		Runs []struct {
			Results []struct {
				RuleID  string `json:"ruleId"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine   int `json:"startLine"`
							StartColumn int `json:"startColumn"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	var out []rvSarifResult
	for _, run := range s.Runs {
		for _, x := range run.Results {
			o := rvSarifResult{Rule: x.RuleID, Level: x.Level, Msg: x.Message.Text}
			if len(x.Locations) > 0 {
				pl := x.Locations[0].PhysicalLocation
				o.URI, o.Line, o.Col = pl.ArtifactLocation.URI, pl.Region.StartLine, pl.Region.StartColumn
			}
			out = append(out, o)
		}
	}
	return out, nil
}

func rvKotlin(c *rvCtx) rvResult {
	var r rvResult
	kt := c.rvFiles("kt")
	if len(kt) == 0 {
		return r
	}
	java := rvJava()
	if java == "" {
		r.notes = append(r.notes, "Kotlin not analysed by detekt (no Java found; install a JDK or Android Studio)")
		return r
	}
	jar, msg, err := rvTool("detekt")
	if msg != "" {
		r.notes = append(r.notes, msg)
	}
	if err != nil {
		r.fails = append(r.fails, "detekt: "+err.Error())
		return r
	}
	cfg := filepath.Join(checkerDir(), "review-detekt.yml")
	os.WriteFile(cfg, []byte(rvDetektConfig), 0o644)
	out := rvTmp("detekt", ".sarif")
	defer os.Remove(out)
	input := c.root
	if c.diff != nil && len(kt) <= 100 {
		input = strings.Join(rvAbsAll(c, kt), ",")
	}
	_, se, _, err := ckRunEnv(ckJavaEnv(), c.root, 10*time.Minute, java, "-jar", jar, "--input", input, "--config", cfg, "--build-upon-default-config",
		"--report", "sarif:"+out, "--parallel", "--excludes", "**/build/**,**/.gradle/**,**/generated/**")
	if err != nil {
		r.fails = append(r.fails, "detekt: "+err.Error())
		return r
	}
	b, rerr := os.ReadFile(out)
	if rerr != nil {
		r.fails = append(r.fails, "detekt: "+ckFirstLines(se, 3))
		return r
	}
	res, err := rvSarif(b)
	if err != nil {
		r.fails = append(r.fails, "detekt: unreadable report")
		return r
	}
	r.ran = append(r.ran, "detekt")
	for _, x := range res {
		if x.Level == "note" || x.Level == "none" {
			continue
		}
		rule := x.Rule
		if i := strings.LastIndex(rule, "."); i >= 0 {
			rule = rule[i+1:]
		}
		sev := rvWarn
		if x.Level == "error" && !strings.Contains(x.Rule, "complexity") {
			sev = rvErr
		}
		rel := rvRel(c.root, x.URI)
		r.add(rvIssue{Tool: "detekt", File: rel, Line: x.Line, Col: x.Col, Sev: sev, Code: rule, Msg: x.Msg})
	}
	return r
}

// ---- complexity: lizard ----

var rvLizardLine = regexp.MustCompile(`^(.+?):(\d+): warning: (.+?) has (\d+) NLOC, (\d+) CCN, (\d+) token, (\d+) PARAM, (\d+) length`)

func rvComplexity(c *rvCtx) rvResult {
	var r rvResult
	var files []string
	for _, f := range c.rvFiles(rvComplexLangs...) {
		if !rvIsTest(f) {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return r
	}
	if !ckExists(ckVenvBin("lizard")) {
		r.notes = append(r.notes, "complexity not measured (lizard needs Python)")
		return r
	}
	args := []string{"-m", "lizard", "-w", "-C", "15", "-L", "120", "-a", "7"}
	if c.diff != nil || c.single != "" {
		if len(files) > 400 {
			files = files[:400]
		}
		args = append(args, rvAbsAll(c, files)...)
	} else {
		for d := range ckSkipDirs {
			args = append(args, "-x", "*/"+d+"/*")
		}
		args = append(args, c.root)
	}
	so, se, _, err := ckRun(c.root, 5*time.Minute, ckVenvBin("python"), args...)
	if err != nil {
		r.fails = append(r.fails, "lizard: "+err.Error())
		return r
	}
	r.ran = append(r.ran, "lizard")
	for _, l := range strings.Split(so+"\n"+se, "\n") {
		m := rvLizardLine.FindStringSubmatch(strings.TrimRight(l, "\r"))
		if m == nil {
			continue
		}
		rel := rvRel(c.root, m[1])
		if rvIsTest(rel) {
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		ccn, _ := strconv.Atoi(m[5])
		params, _ := strconv.Atoi(m[7])
		length, _ := strconv.Atoi(m[8])
		name := m[3]
		if i := strings.Index(name, "@"); i > 0 {
			name = name[:i]
		}
		var why []string
		if ccn > 15 {
			why = append(why, fmt.Sprintf("%d branches (CCN, limit 15)", ccn))
		}
		if length > 120 {
			why = append(why, fmt.Sprintf("%d lines (limit 120)", length))
		}
		if params > 7 {
			why = append(why, fmt.Sprintf("%d parameters (limit 7)", params))
		}
		if len(why) == 0 {
			continue
		}
		sev := rvNote
		if ccn > 25 || length > 150 || params > 8 {
			sev = rvWarn
		}
		r.add(rvIssue{Tool: "lizard", File: rel, Line: ln, end: ln + length - 1, Sev: sev, Code: "complexity",
			Msg: name + " is too big to review or test well: " + strings.Join(why, ", ") + ". Split it into smaller functions."})
	}
	return r
}

// ---- tests: the project's own build and tests ----

var rvJavaHomeBad = regexp.MustCompile(`JAVA_HOME is set to an invalid directory: ([^\r\n|]+)`)

// rvEnvHint recognises a build that failed because of the machine, not the code, and says how to fix it.
func rvEnvHint(s string) string {
	if m := rvJavaHomeBad.FindStringSubmatch(s); m != nil {
		h := "JAVA_HOME points to a folder that doesn't exist (" + strings.TrimSpace(m[1]) + ")"
		if j := rvJava(); j != "" {
			h += "; set it to " + filepath.Dir(filepath.Dir(j))
		}
		return h + ", then restart Claude desktop"
	}
	switch {
	case strings.Contains(s, "SDK location not found"):
		return "the Android SDK isn't found: set ANDROID_HOME or sdk.dir in local.properties"
	case strings.Contains(s, "is not recognized as an internal or external command"), strings.Contains(s, ": command not found"), strings.Contains(s, "executable file not found"):
		return "a build tool is missing from PATH: " + ckFirstLines(s, 2)
	}
	return ""
}

func rvTests(c *rvCtx) rvResult {
	var r rvResult
	if c.single != "" {
		return r
	}
	if c.diff != nil {
		code := false
		for _, k := range c.diff.Order {
			p := c.diff.Files[k].Path
			if rvLang(p) != "" || rvIsDepFile(p) || strings.HasSuffix(strings.ToLower(p), ".gradle") {
				code = true
				break
			}
		}
		if !code && len(c.diff.Deleted) == 0 {
			r.notes = append(r.notes, "tests not run: no code changed")
			return r
		}
	}
	args, _ := json.Marshal(map[string]any{"path": c.root, "max": 12})
	out, err := toolBuild(args)
	if err != nil {
		if strings.Contains(err.Error(), "nothing to build") {
			r.notes = append(r.notes, "no build or tests found to run")
			return r
		}
		r.fails = append(r.fails, "build/tests: "+err.Error())
		return r
	}
	r.ran = append(r.ran, "build+tests")
	lines := strings.Split(out, "\n")
	for i := 0; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], "\r")
		if !strings.HasPrefix(l, "✗ ") {
			continue
		}
		step := strings.TrimPrefix(l, "✗ ")
		var detail []string
		for j := i + 1; j < len(lines) && strings.HasPrefix(lines[j], "  "); j++ {
			detail = append(detail, strings.TrimSpace(lines[j]))
		}
		if hint := rvEnvHint(l + "\n" + strings.Join(detail, "\n")); hint != "" {
			r.fails = append(r.fails, "build/tests couldn't run: "+hint)
			continue
		}
		tool := "build"
		if strings.Contains(strings.ToLower(step), "test") {
			tool = "tests"
		}
		msg := step
		if len(detail) > 0 {
			msg += " — " + strings.Join(detail[:min(len(detail), 4)], " | ")
		}
		r.add(rvIssue{Tool: tool, Sev: rvErr, Code: "failed", Msg: msg, scope: rvScopeAlways})
	}
	return r
}

// ---- ai: an optional second look by the local model ----

const rvAIQuestion = `You are a strict senior code reviewer. The file shows code changes (each line: number, then code; lines marked + were added or changed) and the problems automated tools already reported. Find REAL defects the tools did not report: logic errors, wrong conditions or off-by-one, unhandled edge cases (empty input, nil/None, zero, errors), ignored or swallowed errors, resources never closed, races, security holes, code that does not do what its name or comment says, half-finished code, tests that cannot fail. Ignore style, naming, formatting, comments and everything in the tools list. Answer with at most 8 lines, each exactly in this form:
path:line: high|medium: what is wrong :: the exact code on that line
If nothing is clearly wrong, answer NONE.`

var rvAIClaim = regexp.MustCompile(`(?m)^\s*(?:[-*]\s*|\d+[.)]\s*)?` + "`?" + `([^\s:` + "`" + `][^:\n]*?):(\d+):\s*(high|medium|low)\s*:\s*(.+?)\s*::\s*(.+?)\s*$`)

func rvNorm(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "`\"'")
	return strings.Join(strings.Fields(s), " ")
}

func rvAIPacket(c *rvCtx, issues []rvIssue, budget int) string {
	type cand struct {
		rel  string
		hits int
	}
	var files []cand
	hits := map[string]int{}
	for _, is := range issues {
		hits[rvKey(is.File)]++
	}
	if c.diff != nil {
		for _, k := range c.diff.Order {
			fd := c.diff.Files[k]
			if rvLang(fd.Path) != "" && len(fd.Added) > 0 {
				files = append(files, cand{fd.Path, len(fd.Added)})
			}
		}
	} else {
		for _, l := range rvComplexLangs {
			for _, f := range c.inv.byLang[l] {
				files = append(files, cand{f, hits[rvKey(f)]})
			}
		}
	}
	sort.SliceStable(files, func(i, j int) bool {
		ti, tj := rvIsTest(files[i].rel), rvIsTest(files[j].rel)
		if ti != tj {
			return !ti
		}
		return files[i].hits > files[j].hits
	})
	var b strings.Builder
	cut := false
	for _, f := range files {
		lines, ok := rvReadLines(c.abs(f.rel))
		if !ok || len(lines) == 0 {
			continue
		}
		show := make([]bool, len(lines)+2)
		changed := map[int]bool{}
		if c.diff != nil {
			for n := range c.diff.Files[rvKey(f.rel)].Lines {
				changed[n] = true
				for k := max(1, n-3); k <= min(len(lines), n+3); k++ {
					show[k] = true
				}
			}
		} else {
			for k := 1; k <= len(lines); k++ {
				show[k] = true
			}
		}
		var fb strings.Builder
		fmt.Fprintf(&fb, "=== %s\n", f.rel)
		gap := false
		for k := 1; k <= len(lines); k++ {
			if !show[k] {
				gap = true
				continue
			}
			if gap {
				fb.WriteString("   ...\n")
				gap = false
			}
			mark := " "
			if changed[k] {
				mark = "+"
			}
			t := lines[k-1]
			if len(t) > 240 {
				t = t[:240] + "…"
			}
			fmt.Fprintf(&fb, "%s%5d  %s\n", mark, k, t)
		}
		if b.Len()+fb.Len() > budget {
			cut = true
			continue
		}
		b.WriteString(fb.String())
	}
	if b.Len() == 0 {
		return ""
	}
	if cut {
		b.WriteString("(some files left out to fit)\n")
	}
	b.WriteString("\n=== TOOLS ALREADY REPORTED (do not repeat)\n")
	n := 0
	for _, is := range issues {
		if is.New && is.Sev > rvNote && n < 30 {
			b.WriteString(rvLineText(is) + "\n")
			n++
		}
	}
	if n == 0 {
		b.WriteString("(nothing)\n")
	}
	return b.String()
}

// rvParseAI keeps only claims that point at a real changed line whose code matches.
func rvParseAI(c *rvCtx, answer string) ([]rvIssue, int) {
	var out []rvIssue
	dropped := 0
	for _, m := range rvAIClaim.FindAllStringSubmatch(answer, -1) {
		if m[3] == "low" {
			continue
		}
		rel := rvRel(c.root, strings.Trim(m[1], "`\"' "))
		ln, _ := strconv.Atoi(m[2])
		ev := rvNorm(m[5])
		lines, ok := rvReadLines(c.abs(rel))
		if !ok || ln < 1 || ln > len(lines) || len(ev) < 3 || !c.changed(rel) {
			dropped++
			continue
		}
		at := 0
		for k := max(1, ln-3); k <= min(len(lines), ln+3); k++ {
			t := rvNorm(lines[k-1])
			if t != "" && (strings.Contains(t, ev) || (len(t) >= 6 && strings.Contains(ev, t))) {
				at = k
				break
			}
		}
		if at == 0 {
			dropped++
			continue
		}
		out = append(out, rvIssue{Tool: "ai", File: rel, Line: at, Sev: rvWarn, Code: m[3], Msg: strings.TrimSpace(m[4]) + " (local AI's view: check it)"})
	}
	return out, dropped
}

func rvAIPass(c *rvCtx, issues []rvIssue) rvResult {
	var r rvResult
	packet := rvAIPacket(c, issues, 24000)
	if packet == "" {
		r.notes = append(r.notes, "ai: no changed code to show the local AI")
		return r
	}
	p := rvTmp("ai-review", ".txt")
	if err := os.WriteFile(p, []byte(packet), 0o644); err != nil {
		r.fails = append(r.fails, "ai: "+err.Error())
		return r
	}
	req := AIRequest{Action: "ask", Paths: StrList{p}, Question: rvAIQuestion}
	if rem := rvAIGate(c.a.GPU, c.a.Force, &req); rem != "" {
		r.notes = append(r.notes, "ai: "+rem)
		return r
	}
	j, err := newJob(JobSpec{Name: "review: local AI pass", Kind: "ai", AI: &req, Source: "chat"})
	if err != nil {
		r.fails = append(r.fails, "ai: "+err.Error())
		return r
	}
	j.start()
	waitJob(j, 600)
	s := j.snapshot()
	if s.Status == "running" {
		r.notes = append(r.notes, "ai: the local AI is still reviewing (job "+j.ID+"); its answer will be in that job")
		return r
	}
	if s.Error != "" {
		r.fails = append(r.fails, "ai: "+s.Error)
		return r
	}
	r.ran = append(r.ran, "local AI")
	found, dropped := rvParseAI(c, s.Output)
	r.issues = found
	if dropped > 0 {
		r.notes = append(r.notes, fmt.Sprintf("ai: %d claim(s) dropped because the line or code they named didn't match", dropped))
	}
	return r
}
