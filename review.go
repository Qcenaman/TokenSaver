package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
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

// review (check action "review"): a deep code review of a project or of its latest changes,
// built for code written by AI models (and fine for any other). On top of the check linters it
// runs security rules (Opengrep with cross-function taint, gosec), secret scanning (gitleaks),
// known-vulnerable dependencies (OSV-Scanner) and made-up or brand-new package names,
// GitHub Actions workflows (actionlint, zizmor), shell and PowerShell scripts (ShellCheck,
// PSScriptAnalyzer), Kotlin (detekt), complexity (lizard), the project's own build and tests,
// and checks of its own for unfinished or fake work: elided code, stubs, placeholder values,
// hollow, skipped, focused or deleted tests, removed assertions, new lint suppressions, copied
// blocks, chat text pasted into code and secret files. In a git repo only what changed counts
// (uncommitted changes, else the last commit; base: compares against a branch, tag or commit);
// all:true reviews everything. The verdict is BLOCK (errors), FIX (warnings) or PASS.

type reviewArgs struct {
	checkArgs
	Base   string `json:"base"`
	All    bool   `json:"all"`
	AI     bool   `json:"ai"`
	NoTest bool   `json:"no_test"`
	Skip   string `json:"skip"`
	GPU    string `json:"gpu"`
	Force  bool   `json:"force"`
}

const (
	rvNote = 0
	rvWarn = 1
	rvErr  = 2
)

// When a finding counts as part of the change being reviewed.
const (
	rvScopeLine   = iota // its line changed
	rvScopeFile          // its file changed (type and compile errors often show up away from the edit)
	rvScopeAlways        // always (failing build or tests)
)

type rvIssue struct {
	Tool, File string
	Line, Col  int
	Sev        int
	Code, Msg  string
	New        bool
	scope      int
	end        int // last line of the code a finding is about (a function, a test): new if any of it changed
}

type rvLine struct {
	N int
	T string
}

type rvFileDiff struct {
	Path    string
	New     bool
	Lines   map[int]bool
	Added   []rvLine
	Removed []string
}

type rvDiff struct {
	What        string
	Files       map[string]*rvFileDiff
	Order       []string
	Deleted     []string
	Plus, Minus int
}

type rvInv struct {
	files  []string
	byLang map[string][]string
	wf     []string // GitHub Actions workflow files
	deps   bool     // has dependency manifests or lockfiles
	goMod  bool
	droid  bool
}

type rvCtx struct {
	root, single string
	diff         *rvDiff
	inv          *rvInv
	ign          []string
	a            reviewArgs
	lintPy       bool   // Ruff runs (our own Python checks then leave those cases to it)
	setupErr     string // why the Python/JS linters couldn't be installed
}

type rvResult struct {
	ran, fails, notes []string
	issues            []rvIssue
}

func (r *rvResult) add(is ...rvIssue) { r.issues = append(r.issues, is...) }

func rvKey(p string) string {
	p = strings.TrimPrefix(filepath.ToSlash(p), "./")
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// rvRel turns a path printed by a tool (absolute, relative, file:// URI, either slash) into
// the project-relative slash form used everywhere in a review.
func rvRel(root, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "file:") {
		if u, err := url.Parse(p); err == nil {
			p = u.Path
			if len(p) > 2 && p[0] == '/' && p[2] == ':' {
				p = p[1:]
			}
		}
	}
	p = filepath.FromSlash(p)
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(r, "..") {
			return filepath.ToSlash(r)
		}
		if rr, err := filepath.EvalSymlinks(root); err == nil {
			if pp, err := filepath.EvalSymlinks(p); err == nil {
				if r, err := filepath.Rel(rr, pp); err == nil && !strings.HasPrefix(r, "..") {
					return filepath.ToSlash(r)
				}
			}
		}
		return filepath.ToSlash(p)
	}
	return strings.TrimPrefix(filepath.ToSlash(p), "./")
}

var rvExtLang = map[string]string{
	".py": "py", ".pyi": "py", ".js": "js", ".jsx": "js", ".mjs": "js", ".cjs": "js",
	".ts": "ts", ".tsx": "ts", ".mts": "ts", ".cts": "ts", ".go": "go", ".kt": "kt", ".kts": "kt",
	".java": "java", ".sh": "sh", ".bash": "sh", ".ps1": "ps1", ".psm1": "ps1", ".rs": "rs",
	".c": "c", ".h": "c", ".cc": "c", ".cpp": "c", ".cxx": "c", ".hpp": "c", ".hh": "c",
	".cs": "cs", ".swift": "swift", ".dart": "dart", ".php": "php", ".rb": "rb", ".lua": "lua", ".scala": "scala",
}

func rvLang(p string) string {
	l := strings.ToLower(p)
	if strings.HasSuffix(l, ".min.js") {
		return ""
	}
	return rvExtLang[filepath.Ext(l)]
}

var rvDepFiles = map[string]bool{"pyproject.toml": true, "package.json": true, "build.gradle": true, "build.gradle.kts": true,
	"libs.versions.toml": true, "cargo.toml": true, "go.mod": true, "package-lock.json": true, "yarn.lock": true,
	"pnpm-lock.yaml": true, "poetry.lock": true, "uv.lock": true, "pipfile.lock": true, "cargo.lock": true,
	"gradle.lockfile": true, "pom.xml": true, "composer.lock": true, "gemfile.lock": true, "go.sum": true}

func rvIsDepFile(rel string) bool {
	b := strings.ToLower(filepath.Base(rel))
	return rvDepFiles[b] || (strings.HasPrefix(b, "requirements") && strings.HasSuffix(b, ".txt")) ||
		(strings.HasSuffix(b, ".txt") && strings.Contains(strings.ToLower(filepath.ToSlash(rel)), "requirements/"))
}

var rvExtraSkip = map[string]bool{"target": true, "out": true, "bin": true, "obj": true, ".gradle": true, "vendor": true, ".dart_tool": true, "Pods": true}

func rvInventory(root, single string, ign []string) *rvInv {
	inv := &rvInv{byLang: map[string][]string{}}
	add := func(rel string) {
		inv.files = append(inv.files, rel)
		if l := rvLang(rel); l != "" {
			inv.byLang[l] = append(inv.byLang[l], rel)
		}
		low := strings.ToLower(rel)
		if (strings.HasPrefix(low, ".github/workflows/") || strings.Contains(low, "/.github/workflows/")) && (strings.HasSuffix(low, ".yml") || strings.HasSuffix(low, ".yaml")) {
			inv.wf = append(inv.wf, rel)
		}
		if rvIsDepFile(rel) {
			inv.deps = true
		}
	}
	inv.goMod = ckExists(filepath.Join(root, "go.mod"))
	inv.droid = ckIsAndroid(root)
	if single != "" {
		add(rvRel(root, single))
		return inv
	}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel := ckRel(root, p)
		if d.IsDir() {
			if p != root && (ckSkipDirs[d.Name()] || rvExtraSkip[d.Name()] || ckIgnored(rel+"/", ign)) {
				return filepath.SkipDir
			}
			return nil
		}
		if ckIgnored(rel, ign) {
			return nil
		}
		add(rel)
		if len(inv.files) > 30000 {
			return filepath.SkipAll
		}
		return nil
	})
	return inv
}

// rvFiles is what a layer looks at for one language: the changed files, or every file when
// the whole project is reviewed.
func (c *rvCtx) rvFiles(langs ...string) []string {
	want := map[string]bool{}
	for _, l := range langs {
		want[l] = true
	}
	var out []string
	if c.diff != nil {
		for _, k := range c.diff.Order {
			fd := c.diff.Files[k]
			if want[rvLang(fd.Path)] && ckExists(filepath.Join(c.root, filepath.FromSlash(fd.Path))) {
				out = append(out, fd.Path)
			}
		}
		return out
	}
	for _, l := range langs {
		out = append(out, c.inv.byLang[l]...)
	}
	return out
}

func (c *rvCtx) changed(rel string) bool {
	return c.diff == nil || c.diff.Files[rvKey(rel)] != nil
}

func (c *rvCtx) abs(rel string) string { return filepath.Join(c.root, filepath.FromSlash(rel)) }

// ---- git diff ----

func rvGit(dir string, args ...string) (string, error) {
	git, err := exec.LookPath("git")
	if err != nil {
		return "", errors.New("git not found")
	}
	full := append([]string{"-c", "core.quotePath=false", "-c", "diff.noprefix=false", "-c", "color.ui=false"}, args...)
	so, se, code, err := ckRun(dir, 3*time.Minute, git, full...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return so, fmt.Errorf("git %s: %s", args[0], ckFirstLines(se, 2))
	}
	return so, nil
}

var rvHunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

func rvDiffPath(s, prefix string) string {
	s = strings.TrimRight(s, "\r")
	if i := strings.Index(s, "\t"); i >= 0 {
		s = s[:i]
	}
	if s == "/dev/null" {
		return ""
	}
	if strings.HasPrefix(s, `"`) {
		if u, err := strconv.Unquote(s); err == nil {
			s = u
		}
	}
	return strings.TrimPrefix(s, prefix)
}

func rvParseDiff(s string) *rvDiff {
	d := &rvDiff{Files: map[string]*rvFileDiff{}}
	var cur *rvFileDiff
	inHeader, newFile := false, false
	oldPath := ""
	n := 0
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSuffix(l, "\r")
		switch {
		case strings.HasPrefix(l, "diff --git "):
			cur, inHeader, newFile, oldPath = nil, true, false, ""
		case inHeader && strings.HasPrefix(l, "new file mode"):
			newFile = true
		case inHeader && strings.HasPrefix(l, "--- "):
			oldPath = rvDiffPath(l[4:], "a/")
		case inHeader && strings.HasPrefix(l, "+++ "):
			p := rvDiffPath(l[4:], "b/")
			if p == "" {
				if oldPath != "" {
					d.Deleted = append(d.Deleted, oldPath)
				}
				cur = &rvFileDiff{Path: oldPath, Lines: map[int]bool{}} // deleted file: counted, not kept
				continue
			}
			k := rvKey(p)
			if cur = d.Files[k]; cur == nil {
				cur = &rvFileDiff{Path: p, New: newFile, Lines: map[int]bool{}}
				d.Files[k] = cur
				d.Order = append(d.Order, k)
			}
		case strings.HasPrefix(l, "@@"):
			inHeader = false
			if m := rvHunk.FindStringSubmatch(l); m != nil {
				n, _ = strconv.Atoi(m[1])
			}
		case inHeader || cur == nil:
		case strings.HasPrefix(l, "+"):
			cur.Lines[n] = true
			cur.Added = append(cur.Added, rvLine{n, l[1:]})
			n++
			d.Plus++
		case strings.HasPrefix(l, "-"):
			cur.Removed = append(cur.Removed, l[1:])
			d.Minus++
		case strings.HasPrefix(l, " "):
			n++
		}
	}
	return d
}

// rvAddUntracked adds new files git doesn't track yet (and doesn't ignore) as fully added.
func rvAddUntracked(d *rvDiff, root string) {
	out, err := rvGit(root, "ls-files", "--others", "--exclude-standard", "-z", "--", ".")
	if err != nil {
		return
	}
	for _, p := range strings.Split(out, "\x00") {
		if p = filepath.ToSlash(strings.TrimSpace(p)); p == "" || d.Files[rvKey(p)] != nil {
			continue
		}
		fd := &rvFileDiff{Path: p, New: true, Lines: map[int]bool{}}
		if lines, ok := rvReadLines(filepath.Join(root, filepath.FromSlash(p))); ok {
			for i, t := range lines {
				fd.Lines[i+1] = true
				fd.Added = append(fd.Added, rvLine{i + 1, t})
			}
			d.Plus += len(lines)
		}
		d.Files[rvKey(p)] = fd
		d.Order = append(d.Order, rvKey(p))
	}
}

// rvReadLines reads a text file (max 2 MB); ok is false for binaries and big files.
func rvReadLines(p string) ([]string, bool) {
	st, err := os.Stat(p)
	if err != nil || st.IsDir() || st.Size() > 2<<20 {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil || strings.IndexByte(string(b[:min(len(b), 8000)]), 0) >= 0 {
		return nil, false
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil, true
	}
	return strings.Split(s, "\n"), true
}

func rvIsCommit(root, rev string) bool {
	_, err := rvGit(root, "rev-parse", "--verify", "-q", rev+"^{commit}")
	return err == nil
}

func rvInRepo(root string) bool {
	if _, err := exec.LookPath("git"); err != nil {
		return false
	}
	_, err := rvGit(root, "rev-parse", "--show-toplevel")
	return err == nil
}

func rvMakeDiff(root, base string) (*rvDiff, string, error) {
	if !rvInRepo(root) {
		return nil, "not in a git repo (or git missing): reviewed every file", nil
	}
	hasHead := rvIsCommit(root, "HEAD")
	var args []string
	var what string
	untracked := false
	switch {
	case base != "":
		if !rvIsCommit(root, base) {
			return nil, "", fmt.Errorf("base %q is not a commit, branch or tag in this repo (a shallow copy may not have it)", base)
		}
		args, what, untracked = []string{base}, "changes since "+base, true
	default:
		st, _ := rvGit(root, "status", "--porcelain", "--untracked-files=all", "--", ".")
		if strings.TrimSpace(st) != "" {
			if !hasHead {
				return nil, "no commits yet: reviewed every file", nil
			}
			args, what, untracked = []string{"HEAD"}, "uncommitted changes", true
			break
		}
		if !hasHead {
			return nil, "empty repo: reviewed every file", nil
		}
		if !rvIsCommit(root, "HEAD~1") {
			return nil, "only one commit here: reviewed every file", nil
		}
		args = []string{"HEAD~1", "HEAD"}
		s, _ := rvGit(root, "log", "-1", "--format=%h %s")
		s = strings.TrimSpace(s)
		if len(s) > 70 {
			s = s[:70] + "…"
		}
		what = "last commit " + s
	}
	out, err := rvGit(root, append([]string{"diff", "-U0", "--no-color", "--no-ext-diff", "--relative", "-M", "--src-prefix=a/", "--dst-prefix=b/"}, append(args, "--", ".")...)...)
	if err != nil {
		return nil, "", err
	}
	d := rvParseDiff(out)
	if untracked {
		rvAddUntracked(d, root)
	}
	d.What = what
	return d, "", nil
}

func (d *rvDiff) only(rel string) *rvDiff {
	k := rvKey(rel)
	o := &rvDiff{What: d.What, Files: map[string]*rvFileDiff{}}
	if fd := d.Files[k]; fd != nil {
		o.Files[k] = fd
		o.Order = []string{k}
		o.Plus = len(fd.Added)
		o.Minus = len(fd.Removed)
	}
	return o
}

func (d *rvDiff) isNew(is *rvIssue) bool {
	if d == nil || is.scope == rvScopeAlways {
		return true
	}
	fd := d.Files[rvKey(is.File)]
	if fd == nil {
		return false
	}
	if fd.New || is.Line <= 0 || is.scope == rvScopeFile {
		return true
	}
	for l := is.Line; l <= max(is.Line, is.end); l++ {
		if fd.Lines[l] {
			return true
		}
	}
	return false
}

// ---- running a review ----

type rvLayer struct {
	name string
	run  func(*rvCtx) rvResult
}

func rvSkipped(skip, name string) bool {
	for _, s := range strings.FieldsFunc(strings.ToLower(skip), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if s == name {
			return true
		}
	}
	return false
}

var rvLayers = []rvLayer{
	{"lint", rvLint},
	{"security", rvSecurity},
	{"secrets", rvSecrets},
	{"deps", rvDepsLayer},
	{"ci", rvCI},
	{"scripts", rvScripts},
	{"kotlin", rvKotlin},
	{"complexity", rvComplexity},
	{"slop", rvSlop},
	{"tests", rvTests},
}

func toolReview(raw json.RawMessage) (string, error) {
	var a reviewArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", err
		}
	}
	if runtime.GOOS == "android" {
		return "", errors.New("review runs on the PC; call the PC's TokenSaver with source:\"phone\" to review code on the phone")
	}
	if a.Max <= 0 {
		a.Max = 30
	}
	a.Fix = false
	started := time.Now()
	root, single, label, notes, err := ckTarget(&a.checkArgs)
	if err != nil {
		return "", err
	}
	ign := ckGitIgnored(root)
	c := &rvCtx{root: root, single: single, ign: ign, a: a}
	c.inv = rvInventory(root, single, ign)
	what := "every file"
	if !a.All {
		d, note, err := rvMakeDiff(root, a.Base)
		if err != nil {
			return "", err
		}
		if note != "" {
			notes = append(notes, note)
		}
		if d != nil {
			if single != "" {
				d.What += " in " + filepath.Base(single)
				d = d.only(rvRel(root, single))
			}
			if len(d.Files) == 0 && len(d.Deleted) == 0 {
				return fmt.Sprintf("Review of %s: nothing changed (%s). all:true reviews every file; base:\"main\" (or a tag/commit) reviews everything since then.", label, d.What), nil
			}
			c.diff = d
			what = fmt.Sprintf("%s: %d file(s), +%d −%d", d.What, len(d.Files)+len(d.Deleted), d.Plus, d.Minus)
		}
	}
	installMsgs := rvPrepare(c)

	var mu sync.Mutex
	var all []rvResult
	sem := make(chan struct{}, max(2, min(4, runtime.NumCPU()/2)))
	var wg sync.WaitGroup
	for _, l := range rvLayers {
		if rvSkipped(a.Skip, l.name) || l.name == "tests" {
			continue
		}
		wg.Add(1)
		go func(l rvLayer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var r rvResult
			func() {
				defer func() {
					if p := recover(); p != nil {
						r = rvResult{fails: []string{fmt.Sprintf("%s: internal error: %v", l.name, p)}}
					}
				}()
				r = l.run(c)
			}()
			mu.Lock()
			all = append(all, r)
			mu.Unlock()
		}(l)
	}
	wg.Wait()
	// the build and tests run alone: two Gradle runs in one project block each other, and
	// timing-sensitive tests shouldn't compete with the analysers for the CPU
	if !a.NoTest && !rvSkipped(a.Skip, "tests") {
		all = append(all, rvTests(c))
	}
	var issues []rvIssue
	var ran, fails []string
	for _, r := range all {
		issues = append(issues, r.issues...)
		ran = append(ran, r.ran...)
		fails = append(fails, r.fails...)
		notes = append(notes, r.notes...)
	}
	issues = rvDedupe(issues)
	for i := range issues {
		issues[i].New = c.diff.isNew(&issues[i])
	}
	if a.AI && !rvSkipped(a.Skip, "ai") {
		r := rvAIPass(c, issues)
		for i := range r.issues {
			r.issues[i].New = c.diff.isNew(&r.issues[i])
		}
		issues = append(issues, r.issues...)
		ran = append(ran, r.ran...)
		fails = append(fails, r.fails...)
		notes = append(notes, r.notes...)
	}
	return rvFormat(c, label, what, issues, ran, fails, append(installMsgs, notes...), time.Since(started)), nil
}

// rvDedupe drops the same finding reported twice (one tool through two routes, or two tools
// with the same rule), keeping the more severe one.
func rvDedupe(in []rvIssue) []rvIssue {
	type key struct {
		file string
		line int
		code string
	}
	best := map[key]int{}
	var out []rvIssue
	for _, is := range in {
		k := key{rvKey(is.File), is.Line, strings.ToLower(is.Code)}
		if i, ok := best[k]; ok && is.Code != "" {
			if is.Sev > out[i].Sev {
				out[i] = is
			}
			continue
		}
		best[k] = len(out)
		out = append(out, is)
	}
	return out
}

var rvSevName = map[int]string{rvErr: "error", rvWarn: "warning", rvNote: "note"}

func rvLineText(is rvIssue) string {
	msg := strings.Join(strings.Fields(is.Msg), " ")
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	loc := is.File
	if loc == "" {
		loc = "(project)"
	} else if is.Line > 0 {
		loc += ":" + strconv.Itoa(is.Line)
	}
	code := is.Tool
	if is.Code != "" && is.Code != is.Tool {
		code += " " + is.Code
	}
	return loc + " " + code + ": " + msg
}

func rvFormat(c *rvCtx, label, what string, issues []rvIssue, ran, fails, notes []string, took time.Duration) string {
	sort.SliceStable(issues, func(i, j int) bool {
		x, y := issues[i], issues[j]
		if x.New != y.New {
			return x.New
		}
		if x.Sev != y.Sev {
			return x.Sev > y.Sev
		}
		if x.File != y.File {
			return x.File < y.File
		}
		return x.Line < y.Line
	})
	var nErr, nWarn, nNote, oldErr, oldWarn int
	for _, is := range issues {
		switch {
		case is.New && is.Sev == rvErr:
			nErr++
		case is.New && is.Sev == rvWarn:
			nWarn++
		case is.New:
			nNote++
		case is.Sev == rvErr:
			oldErr++
		case is.Sev == rvWarn:
			oldWarn++
		}
	}
	verdict := "PASS"
	switch {
	case nErr > 0:
		verdict = "BLOCK"
	case nWarn > 0:
		verdict = "FIX"
	}
	scope := "in the changes"
	if c.diff == nil {
		scope = "in the project"
	}
	head := fmt.Sprintf("Verdict: %s — %d error(s), %d warning(s) %s", verdict, nErr, nWarn, scope)
	if oldErr+oldWarn > 0 {
		head += fmt.Sprintf(" · older code: %d error(s), %d warning(s) not shown", oldErr, oldWarn)
	}
	if len(fails) > 0 {
		head += " · incomplete: " + strconv.Itoa(len(fails)) + " tool(s) couldn't run"
	}
	dir := filepath.Join(checkerDir(), "reports")
	os.MkdirAll(dir, 0o755)
	stamp := time.Now().Format("20060102-150405")
	report := filepath.Join(dir, "review-"+stamp+".txt")

	var b, full strings.Builder
	fmt.Fprintf(&b, "Review of %s (%s, %s)\n%s\nFull report: %s\n", label, what, took.Round(time.Second), head, report)
	fmt.Fprintf(&full, "Review of %s\nwhen: %s (%s)\nroot: %s\nscope: %s\n%s\n", label, time.Now().Format(time.RFC3339), took.Round(time.Second), c.root, what, head)
	shown := 0
	section := func(title string, keep func(rvIssue) bool, inShort func(rvIssue) bool) {
		var lines, short []string
		for i, is := range issues {
			if !keep(is) {
				continue
			}
			lines = append(lines, rvLineText(is))
			if !inShort(is) {
				continue
			}
			// the short list folds other findings on the same line into the first one
			if i > 0 && keep(issues[i-1]) && issues[i-1].File == is.File && issues[i-1].Line == is.Line && is.Line > 0 && len(short) > 0 {
				tag := is.Tool
				if is.Code != "" {
					tag += " " + is.Code
				}
				last := short[len(short)-1]
				if strings.HasSuffix(last, "]") && strings.Contains(last, " [also: ") {
					short[len(short)-1] = last[:len(last)-1] + ", " + tag + "]"
				} else {
					short[len(short)-1] = last + " [also: " + tag + "]"
				}
				continue
			}
			short = append(short, rvLineText(is))
		}
		if len(lines) == 0 {
			return
		}
		fmt.Fprintf(&full, "\n%s (%d):\n", title, len(lines))
		for _, l := range lines {
			full.WriteString("  " + l + "\n")
		}
		if shown >= c.a.Max || len(short) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s (%d):\n", title, len(short))
		for _, l := range short {
			if shown >= c.a.Max {
				fmt.Fprintf(&b, "  … more in the full report\n")
				break
			}
			b.WriteString("  " + l + "\n")
			shown++
		}
	}
	all := func(rvIssue) bool { return true }
	section("Errors", func(is rvIssue) bool { return is.New && is.Sev == rvErr }, all)
	section("Warnings", func(is rvIssue) bool { return is.New && is.Sev == rvWarn }, all)
	section("Notes", func(is rvIssue) bool { return is.New && is.Sev == rvNote }, func(is rvIssue) bool { return is.Code == "no-tests" })
	if c.diff != nil {
		var older []string
		for _, is := range issues {
			if !is.New && is.Sev > rvNote {
				older = append(older, rvLineText(is))
			}
		}
		if len(older) > 0 {
			fmt.Fprintf(&full, "\nOlder code, not part of this change (%d):\n", len(older))
			for _, l := range older {
				full.WriteString("  " + l + "\n")
			}
		}
	}
	sort.Strings(ran)
	ran = rvUniq(ran)
	if len(ran) > 0 {
		b.WriteString("Ran: " + strings.Join(ran, ", ") + "\n")
		full.WriteString("\nRan: " + strings.Join(ran, ", ") + "\n")
	}
	for _, f := range fails {
		b.WriteString("⚠ " + f + "\n")
		full.WriteString("⚠ " + f + "\n")
	}
	var installed, other []string
	for _, n := range rvUniq(notes) {
		switch {
		case n == "":
		case strings.HasPrefix(n, "installed "):
			installed = append(installed, strings.TrimPrefix(n, "installed "))
		default:
			other = append(other, n)
		}
	}
	if len(installed) > 0 {
		other = append([]string{"Installed on first use: " + strings.Join(installed, ", ")}, other...)
	}
	for _, n := range other {
		b.WriteString(n + "\n")
		full.WriteString(n + "\n")
	}
	if verdict == "PASS" && len(fails) == 0 {
		b.WriteString("No problems found in what was reviewed.\n")
	}
	os.WriteFile(report, []byte(full.String()), 0o644)
	os.WriteFile(filepath.Join(checkerDir(), "last-review.txt"), []byte(full.String()), 0o644)
	type jIssue struct {
		Tool     string `json:"tool"`
		File     string `json:"file,omitempty"`
		Line     int    `json:"line,omitempty"`
		Col      int    `json:"col,omitempty"`
		Severity string `json:"severity"`
		Code     string `json:"code,omitempty"`
		Message  string `json:"message"`
		New      bool   `json:"in_change"`
	}
	js := struct {
		Target  string   `json:"target"`
		Root    string   `json:"root"`
		Scope   string   `json:"scope"`
		Verdict string   `json:"verdict"`
		Issues  []jIssue `json:"issues"`
		Ran     []string `json:"ran"`
		Failed  []string `json:"failed,omitempty"`
	}{label, c.root, what, verdict, []jIssue{}, ran, fails}
	for _, is := range issues {
		js.Issues = append(js.Issues, jIssue{is.Tool, is.File, is.Line, is.Col, rvSevName[is.Sev], is.Code, is.Msg, is.New})
	}
	if jb, err := json.MarshalIndent(js, "", " "); err == nil {
		os.WriteFile(filepath.Join(checkerDir(), "last-review.json"), jb, 0o644)
	}
	rvPrune(dir, 20)
	return strings.TrimRight(b.String(), "\n")
}

func rvUniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// rvPrune keeps the newest keep review reports.
func rvPrune(dir string, keep int) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "review-") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// rvIsTest says whether a project file is test code.
func rvIsTest(rel string) bool {
	p := "/" + strings.ToLower(filepath.ToSlash(rel))
	b := filepath.Base(p)
	switch {
	case strings.Contains(p, "/test/"), strings.Contains(p, "/tests/"), strings.Contains(p, "/__tests__/"),
		strings.Contains(p, "/testing/"), strings.Contains(p, "/androidtest/"), strings.Contains(p, "/testdata/"),
		strings.Contains(p, "/spec/"), strings.Contains(p, "/e2e/"):
		return true
	case strings.HasPrefix(b, "test_"), strings.HasSuffix(b, "_test.py"), strings.HasSuffix(b, "_test.go"), b == "conftest.py",
		strings.Contains(b, ".test."), strings.Contains(b, ".spec."), strings.HasSuffix(b, "_spec.rb"):
		return true
	}
	for _, s := range []string{"test.kt", "tests.kt", "test.java", "tests.java", "spec.kt", "tests.cs", "test.cs", "tests.swift", "test.swift", "_test.dart"} {
		if strings.HasSuffix(b, s) && len(b) > len(s) {
			return true
		}
	}
	return false
}
