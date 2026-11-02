package main

// git compare: two folders or repo copies (owner/name[@ref]) file by file, so two
// versions, or an app and its port, can be compared without reading them: files only
// on one side, changed files by changed lines, how many are the same, and the changed
// lines of chosen files.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var cmpSkipDirs = map[string]bool{".git": true, "vendor": true, "node_modules": true, "build": true, "dist": true,
	"out": true, "bin": true, "obj": true, "target": true, "__pycache__": true, ".gradle": true, ".next": true,
	".venv": true, "venv": true, ".idea": true, ".vs": true, ".mypy_cache": true, ".ruff_cache": true, ".pytest_cache": true}

var cmpSkipExts = map[string]bool{".exe": true, ".dll": true, ".so": true, ".dylib": true, ".o": true, ".obj": true,
	".a": true, ".lib": true, ".class": true, ".pyc": true, ".pdb": true, ".apk": true}

const cmpMaxRead = 8 << 20

type cmpSide struct {
	label, root string
	files       map[string]string // relative path with slashes -> full path
	copy        *repoCopy
}

func cmpOpen(p string) (*cmpSide, error) {
	s := &cmpSide{}
	if c, ok, err := repoResolve(p); ok {
		if err != nil {
			return nil, err
		}
		s.label, s.root, s.copy = c.Label, c.Local, c
		if c.Sub != "" {
			s.label += "/" + c.Sub
		}
	} else {
		root, err := expandPath(p)
		if err != nil {
			return nil, err
		}
		if err := checkReadable(root); err != nil {
			return nil, err
		}
		s.label, s.root = root, root
	}
	if st, err := os.Stat(s.root); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("not a folder: %s", s.label)
	}
	s.files = cmpList(s.root)
	return s, nil
}

func cmpKeep(rel string) bool {
	parts := strings.Split(rel, "/")
	for _, d := range parts[:len(parts)-1] {
		if cmpSkipDirs[strings.ToLower(d)] {
			return false
		}
	}
	return !cmpSkipExts[strings.ToLower(path.Ext(rel))]
}

// cmpList lists a folder's files: in a git work tree what git tracks plus new files it
// doesn't ignore, else everything; dependency, build and .git folders left out.
func cmpList(root string) map[string]string {
	files := map[string]string{}
	if out, code, err := gitRaw(root, 2*time.Minute, "ls-files", "-z", "--cached", "--others", "--exclude-standard"); err == nil && code == 0 {
		for _, rel := range strings.Split(out, "\x00") {
			if rel == "" || !cmpKeep(rel) {
				continue
			}
			full := filepath.Join(root, filepath.FromSlash(rel))
			if fi, err := os.Lstat(full); err == nil && fi.Mode().IsRegular() {
				files[rel] = full
			}
		}
		if len(files) > 0 {
			return files
		}
	}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && cmpSkipDirs[strings.ToLower(d.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil && cmpKeep(filepath.ToSlash(rel)) {
			files[filepath.ToSlash(rel)] = p
		}
		return nil
	})
	return files
}

// gitRaw runs git and returns stdout untouched (gitExec trims it and drops text before a \r).
func gitRaw(dir string, timeout time.Duration, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=auto")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return string(out), ee.ExitCode(), nil
		}
		return "", -1, err
	}
	return string(out), 0, nil
}

func cmpBinary(d []byte) bool { return bytes.IndexByte(d[:min(len(d), 8000)], 0) >= 0 }

// cmpLines splits text into lines, CRLF and a missing last newline not counting.
func cmpLines(d []byte) []string {
	s := strings.TrimSuffix(strings.ReplaceAll(string(d), "\r\n", "\n"), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func sameContent(pa, pb string) bool {
	fa, err := os.Open(pa)
	if err != nil {
		return false
	}
	defer fa.Close()
	fb, err := os.Open(pb)
	if err != nil {
		return false
	}
	defer fb.Close()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if !bytes.Equal(ba[:na], bb[:nb]) {
			return false
		}
		if ea != nil || eb != nil {
			return ea != nil && eb != nil
		}
	}
}

// cmpFiles compares two files; changed text files get counts of added and removed lines.
func cmpFiles(pa, pb string) (same bool, add, del int, binary bool, err error) {
	sa, err := os.Stat(pa)
	if err != nil {
		return
	}
	sb, err := os.Stat(pb)
	if err != nil {
		return
	}
	if sa.Size() > cmpMaxRead || sb.Size() > cmpMaxRead {
		return sa.Size() == sb.Size() && sameContent(pa, pb), 0, 0, true, nil
	}
	da, err := os.ReadFile(pa)
	if err != nil {
		return
	}
	db, err := os.ReadFile(pb)
	if err != nil {
		return
	}
	if bytes.Equal(da, db) {
		return true, 0, 0, false, nil
	}
	if cmpBinary(da) || cmpBinary(db) {
		return false, 0, 0, true, nil
	}
	add, del = lineDiffCount(cmpLines(da), cmpLines(db))
	return add == 0 && del == 0, add, del, false, nil
}

// lineDiffCount counts lines added and removed between a and b (shortest edit script);
// files too different to diff in good time get the count of lines missing on each side.
func lineDiffCount(a, b []string) (add, del int) {
	for len(a) > 0 && len(b) > 0 && a[0] == b[0] {
		a, b = a[1:], b[1:]
	}
	for len(a) > 0 && len(b) > 0 && a[len(a)-1] == b[len(b)-1] {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	n, m := len(a), len(b)
	if n == 0 || m == 0 {
		return m, n
	}
	ids := map[string]int{}
	intern := func(l []string) []int {
		out := make([]int, len(l))
		for i, s := range l {
			id, ok := ids[s]
			if !ok {
				id = len(ids)
				ids[s] = id
			}
			out[i] = id
		}
		return out
	}
	ia, ib := intern(a), intern(b)
	if d := myersD(ia, ib, min(n+m, 40_000_000/(n+m))); d >= 0 {
		lcs := (n + m - d) / 2
		return m - lcs, n - lcs
	}
	cnt := map[int]int{}
	for _, x := range ia {
		cnt[x]++
	}
	for _, x := range ib {
		cnt[x]--
	}
	for _, c := range cnt {
		if c > 0 {
			del += c
		} else {
			add -= c
		}
	}
	return add, del
}

// myersD is the edit distance (inserts + deletes) between a and b, or -1 when it is
// larger than maxD (Myers' O((n+m)D) algorithm).
func myersD(a, b []int, maxD int) int {
	n, m := len(a), len(b)
	off := maxD + 1
	v := make([]int, 2*maxD+3)
	for d := 0; d <= maxD; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y >= 0 && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[off+k] = x
			if x >= n && y >= m {
				return d
			}
		}
	}
	return -1
}

var platTokens = map[string]int{"windows": 1, "linux": 1, "darwin": 1, "macos": 1, "win": 1, "win32": 1,
	"android": 1, "ios": 1, "freebsd": 1, "unix": 2, "posix": 2, "nix": 2, "bsd": 2, "other": 3}

// platKey strips a platform part from a file name (x_windows.go, x_linux_test.go,
// x.windows.ts, x-linux.sh) so a port's files can be matched; rank puts a platform
// file first, _unix/_other style fallbacks next and the plain file last.
func platKey(rel string) (string, int) {
	dir, file := path.Split(rel)
	ext := path.Ext(file)
	stem := strings.TrimSuffix(file, ext)
	test := ""
	if strings.HasSuffix(stem, "_test") {
		stem, test = strings.TrimSuffix(stem, "_test"), "_test"
	}
	if i := strings.LastIndexAny(stem, "_.-"); i > 0 {
		if r, ok := platTokens[strings.ToLower(stem[i+1:])]; ok {
			return dir + stem[:i] + test + ext, r
		}
	}
	return dir + stem + test + ext, 4
}

// cmpPairs matches files found on one side only by platKey (x_windows.go with
// x_linux.go or x.go), so a port compares file by file.
func cmpPairs(onlyA, onlyB []string) (pairs [][2]string, restA, restB []string) {
	group := func(l []string) map[string][]string {
		g := map[string][]string{}
		for _, f := range l {
			k, _ := platKey(f)
			g[k] = append(g[k], f)
		}
		for _, v := range g {
			sort.SliceStable(v, func(i, j int) bool {
				_, ri := platKey(v[i])
				_, rj := platKey(v[j])
				return ri < rj
			})
		}
		return g
	}
	ga, gb := group(onlyA), group(onlyB)
	paired := map[string]bool{}
	for k, la := range ga {
		lb := gb[k]
		for i := 0; i < len(la) && i < len(lb); i++ {
			pairs = append(pairs, [2]string{la[i], lb[i]})
			paired["a\x00"+la[i]], paired["b\x00"+lb[i]] = true, true
		}
	}
	for _, f := range onlyA {
		if !paired["a\x00"+f] {
			restA = append(restA, f)
		}
	}
	for _, f := range onlyB {
		if !paired["b\x00"+f] {
			restB = append(restB, f)
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i][0] < pairs[j][0] })
	return pairs, restA, restB
}

// cmpDiffText is git's diff of two files (line endings ignored) under short names.
func cmpDiffText(pa, pb, ra, rb string) string {
	out, code, err := gitRaw(filepath.Dir(pa), time.Minute, "diff", "--no-index", "--no-color", "--ignore-cr-at-eol", "-U3", "--", pa, pb)
	if err != nil {
		return ra + ": " + err.Error()
	}
	if code == 0 {
		return ra + ": same"
	}
	lines := strings.Split(strings.ReplaceAll(out, "\r", ""), "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "@@") {
			return "--- A/" + ra + "\n+++ B/" + rb + "\n" + strings.TrimRight(strings.Join(lines[i:], "\n"), "\n")
		}
		if strings.HasPrefix(l, "Binary files") {
			return ra + ": binary files differ"
		}
	}
	return ra + ": same apart from line endings"
}

func gitCompare(a gitArgs, maxChars int) (string, error) {
	if strings.TrimSpace(a.Path) == "" || strings.TrimSpace(a.To) == "" {
		return "", errors.New("compare needs path and to: folders or owner/name[@ref]")
	}
	A, err := cmpOpen(a.Path)
	if err != nil {
		return "", err
	}
	B, err := cmpOpen(a.To)
	if err != nil {
		return "", err
	}
	n := a.N
	if n <= 0 {
		n = 40
	}
	var common, onlyA, onlyB []string
	for rel := range A.files {
		if _, ok := B.files[rel]; ok {
			common = append(common, rel)
		} else {
			onlyA = append(onlyA, rel)
		}
	}
	for rel := range B.files {
		if _, ok := A.files[rel]; !ok {
			onlyB = append(onlyB, rel)
		}
	}
	sort.Strings(common)
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	var pairs [][2]string
	if a.Pair {
		pairs, onlyA, onlyB = cmpPairs(onlyA, onlyB)
	}
	type item struct {
		name, pa, pb string
		add, del     int
		same, binary bool
		err          error
	}
	items := make([]item, 0, len(common)+len(pairs))
	for _, rel := range common {
		items = append(items, item{name: rel, pa: A.files[rel], pb: B.files[rel]})
	}
	for _, p := range pairs {
		items = append(items, item{name: p[0] + " ↔ " + p[1], pa: A.files[p[0]], pb: B.files[p[1]]})
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				it := &items[i]
				it.same, it.add, it.del, it.binary, it.err = cmpFiles(it.pa, it.pb)
			}
		}()
	}
	for i := range items {
		next <- i
	}
	close(next)
	wg.Wait()
	var changed []item
	same := 0
	for _, it := range items {
		if it.err == nil && it.same {
			same++
		} else {
			changed = append(changed, it)
		}
	}
	sort.SliceStable(changed, func(i, j int) bool {
		return changed[i].add+changed[i].del > changed[j].add+changed[j].del
	})

	var b strings.Builder
	fmt.Fprintf(&b, "A: %s\nB: %s\n", A.label, B.label)
	list := func(title string, l []string) {
		if len(l) == 0 {
			return
		}
		shown, more := l, ""
		if len(l) > n {
			shown, more = l[:n], fmt.Sprintf(", … %d more", len(l)-n)
		}
		fmt.Fprintf(&b, "%s (%d): %s%s\n", title, len(l), strings.Join(shown, ", "), more)
	}
	list("Only in A", onlyA)
	list("Only in B", onlyB)
	if len(changed) > 0 {
		fmt.Fprintf(&b, "Changed (%d), most changed first:\n", len(changed))
		for i, it := range changed {
			if i == n {
				fmt.Fprintf(&b, "  … %d more\n", len(changed)-n)
				break
			}
			switch {
			case it.err != nil:
				fmt.Fprintf(&b, "  %s (%s)\n", it.name, cleanErr(it.err))
			case it.binary:
				fmt.Fprintf(&b, "  %s (binary or large)\n", it.name)
			default:
				fmt.Fprintf(&b, "  %s +%d -%d\n", it.name, it.add, it.del)
			}
		}
	}
	s := "s"
	if same == 1 {
		s = ""
	}
	fmt.Fprintf(&b, "Same: %d file%s\n", same, s)

	if a.Full && len(a.Paths) > 0 {
		pairOf := map[string][2]string{}
		for _, p := range pairs {
			pairOf[p[0]], pairOf[p[1]] = p, p
		}
		var d strings.Builder
		for _, want := range a.Paths {
			rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(want)), "./")
			ra, rb := rel, rel
			if p, ok := pairOf[rel]; ok {
				ra, rb = p[0], p[1]
			}
			pa, inA := A.files[ra]
			pb, inB := B.files[rb]
			switch {
			case inA && inB:
				d.WriteString(cmpDiffText(pa, pb, ra, rb) + "\n")
			case inA:
				fmt.Fprintf(&d, "%s: only in A\n", rel)
			case inB:
				fmt.Fprintf(&d, "%s: only in B\n", rel)
			default:
				fmt.Fprintf(&d, "%s: on neither side\n", rel)
			}
		}
		full := d.String()
		if len([]rune(full)) > maxChars {
			full = clip(full, maxChars) + fmt.Sprintf("\n… cut at %d chars (max_chars)", maxChars)
		}
		b.WriteString("\n" + full)
	}
	out := strings.TrimRight(b.String(), "\n")
	var cs []*repoCopy
	for _, side := range []*cmpSide{A, B} {
		if side.copy != nil {
			cs = append(cs, side.copy)
		}
	}
	if len(cs) > 0 {
		out = repoFixer(cs)(out)
	}
	return out, nil
}
