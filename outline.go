package main

// outline: functions and types with line numbers for a code file or project, so only
// the lines needed get read. universal-ctags (100+ languages) when it is installed,
// else a regex pass over the common languages.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var outlineKinds = map[string]bool{"function": true, "func": true, "method": true, "class": true, "struct": true,
	"interface": true, "type": true, "enum": true, "trait": true, "impl": true, "object": true, "module": true,
	"namespace": true, "typedef": true, "constant": true}

type outlineSym struct {
	file, kind, name, sig, scope string
	line                         int
}

func toolOutline(raw json.RawMessage) (string, error) {
	var a struct {
		Path     string `json:"path"`
		Name     string `json:"name"`
		MaxChars int    `json:"max_chars"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("bad arguments: %v", err)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("outline needs path: a code file or project folder, or owner/name[@ref][/sub]")
	}
	p, err := expandPath(a.Path)
	if err != nil {
		return "", err
	}
	if err := checkReadable(p); err != nil {
		return "", err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", errors.New(cleanErr(err) + ": " + p)
	}
	syms, err := ctagsSymbols(p)
	if err != nil || len(syms) == 0 {
		syms = regexSymbols(p)
	}
	if len(syms) == 0 {
		return "no functions or types found in " + p, nil
	}
	sort.SliceStable(syms, func(i, j int) bool {
		if syms[i].file != syms[j].file {
			return syms[i].file < syms[j].file
		}
		return syms[i].line < syms[j].line
	})
	filter := strings.ToLower(a.Name)
	var b strings.Builder
	last := ""
	for _, s := range syms {
		if filter != "" && !strings.Contains(strings.ToLower(s.name), filter) {
			continue
		}
		if s.file != last {
			rel := filepath.Base(s.file)
			if st.IsDir() {
				if r, err := filepath.Rel(p, s.file); err == nil {
					rel = filepath.ToSlash(r)
				}
			}
			b.WriteString(rel + "\n")
			last = s.file
		}
		name := s.name
		if s.scope != "" {
			name = s.scope + "." + name
		}
		fmt.Fprintf(&b, "  %d %s %s%s\n", s.line, outlineKind(s.kind), name, s.sig)
	}
	out := strings.TrimRight(b.String(), "\n")
	if out == "" {
		return fmt.Sprintf("nothing named like %q in %s", a.Name, p), nil
	}
	limit := a.MaxChars
	if limit <= 0 {
		limit = 8000
	}
	if len([]rune(out)) > limit {
		out = clip(out, limit) + fmt.Sprintf("\n… cut at %d chars: pass name to filter, or a sub folder", limit)
	}
	return out, nil
}

func outlineKind(k string) string {
	switch k {
	case "function", "func":
		return "func"
	case "method":
		return "meth"
	case "constant":
		return "const"
	}
	return k
}

// ctagsSymbols uses universal-ctags' JSON output, skipping dependency and build folders.
func ctagsSymbols(p string) ([]outlineSym, error) {
	ct, err := exec.LookPath("ctags")
	if err != nil {
		return nil, err
	}
	args := []string{"-R", "--output-format=json", "--fields=+nKSZ", "--extras=-F", "-f", "-"}
	for d := range cmpSkipDirs {
		args = append(args, "--exclude="+d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, ct, append(args, p)...)
	hideWindow(c)
	out, err := c.Output()
	if err != nil && len(out) == 0 {
		return nil, err
	}
	var syms []outlineSym
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var t struct {
			Type      string `json:"_type"`
			Name      string `json:"name"`
			Path      string `json:"path"`
			Kind      string `json:"kind"`
			Signature string `json:"signature"`
			Scope     string `json:"scope"`
			ScopeKind string `json:"scopeKind"`
			Line      int    `json:"line"`
		}
		if json.Unmarshal(sc.Bytes(), &t) != nil || t.Type != "tag" || !outlineKinds[t.Kind] {
			continue
		}
		scope := t.Scope
		switch t.ScopeKind {
		case "package", "namespace", "module", "file", "":
			scope = "" // the file already says where it is
		default:
			scope = scope[strings.LastIndexAny(scope, ".:")+1:]
		}
		syms = append(syms, outlineSym{t.Path, t.Kind, t.Name, t.Signature, scope, t.Line})
	}
	return syms, nil
}

// symRes: a plain fallback for the common languages when ctags isn't installed.
var symRes = map[string]*regexp.Regexp{
	".go":   regexp.MustCompile(`^(func(?:\s+\([^)]*\))?\s+(\w+)(\(.*?\))|type\s+(\w+)\s+(struct|interface|\w+))`),
	".py":   regexp.MustCompile(`^\s*(?:async\s+)?(def|class)\s+(\w+)(\(.*?\))?`),
	".js":   regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?(function\*?\s+(\w+)(\(.*?\))|class\s+(\w+)|(?:const|let)\s+(\w+)\s*=\s*(?:async\s*)?\(.*?\)\s*=>)`),
	".kt":   regexp.MustCompile(`^\s*(?:(?:private|public|internal|override|suspend|open|data|abstract)\s+)*(fun\s+(?:<[^>]*>\s*)?(\w+)(\(.*?\))|(?:class|object|interface)\s+(\w+))`),
	".java": regexp.MustCompile(`^\s*(?:(?:public|private|protected|static|final|abstract)\s+)+(?:class|interface|enum|[\w<>\[\]]+)\s+(\w+)\s*(\(.*?\))?`),
	".rs":   regexp.MustCompile(`^\s*(?:pub(?:\(\w+\))?\s+)?(?:async\s+)?(fn\s+(\w+)|struct\s+(\w+)|enum\s+(\w+)|trait\s+(\w+)|impl\b[^{]*)`),
	".c":    regexp.MustCompile(`^[A-Za-z_][\w\s\*]*\s\**(\w+)\s*\([^;]*$`),
}

var symWordRe = regexp.MustCompile(`^\w+$`)

func regexSymbols(p string) []outlineSym {
	var out []outlineSym
	scan := func(f string) {
		ext := strings.ToLower(filepath.Ext(f))
		switch ext {
		case ".ts", ".tsx", ".jsx", ".mjs", ".cjs":
			ext = ".js"
		case ".cpp", ".cc", ".cxx", ".h", ".hpp":
			ext = ".c"
		case ".kts":
			ext = ".kt"
		}
		re := symRes[ext]
		if re == nil {
			return
		}
		fh, err := os.Open(f)
		if err != nil {
			return
		}
		defer fh.Close()
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for n := 1; sc.Scan(); n++ {
			m := re.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			name := ""
			for _, g := range m[2:] {
				if g != "" && symWordRe.MatchString(g) {
					name = g
					break
				}
			}
			if name == "" || name == "struct" || name == "interface" {
				name = strings.TrimSpace(m[1])
			}
			kind := "def"
			if ext != ".c" && ext != ".java" {
				if fs := strings.Fields(m[1]); len(fs) > 0 && symWordRe.MatchString(fs[0]) {
					kind = fs[0]
				}
			}
			out = append(out, outlineSym{f, kind, name, "", "", n})
		}
	}
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		scan(p)
		return out
	}
	filepath.WalkDir(p, func(f string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if f != p && (strings.HasPrefix(d.Name(), ".") || cmpSkipDirs[strings.ToLower(d.Name())]) {
				return filepath.SkipDir
			}
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.Size() < 2<<20 {
			scan(f)
		}
		return nil
	})
	return out
}
