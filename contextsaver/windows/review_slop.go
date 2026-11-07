package main

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Checks of our own for what AI models (and hurried people) leave behind: code elided with
// "rest of the code here", stubs and placeholder values, tests that can't fail or were
// skipped, focused, deleted or emptied of assertions, new lint suppressions, copied blocks,
// chat text pasted into code, conflict markers, secret files and build output added to git.

type rvRule struct {
	code string
	sev  int
	re   *regexp.Regexp
	msg  string
}

func rx(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// Rules on comment text.
var rvCommentRules = []rvRule{
	{"elided-code", rvErr, rx(`(?i)(\.\.\.|…)\s*\(?\s*(the\s+)?(rest|remainder|remaining|existing|other|previous|unchanged|original|same)\b|\b(rest|remainder) of (the )?(code|implementation|file|function|method|class|logic|tests?|methods|functions)\b|\bexisting (code|implementation|logic|methods|functions)( (here|unchanged|remains|stays|goes|as before)| (is|are) unchanged)\b|\b(code|implementation|logic|everything else) (remains|stays|is) (the )?(same|unchanged)\b|\bunchanged (code|lines|methods|functions)\b`),
		"code was left out (\"rest of the code\"/\"...\"): write the real code instead of a placeholder comment"},
	{"unfinished", rvErr, rx(`(?i)\b(your|add|insert|put|write) (own )?(code|logic|implementation)( goes)? here\b|\bimplementation (goes )?here\b|\bimplement (this|me|later|properly)\b|\b(is|isn't|is not|not) (yet )?implemented( yet)?\b|\bplaceholder (implementation|code|logic|function|body|for now)\b|\b(this is|just) a placeholder\b|\bplaceholder\s*$|\bstub(bed)?( (implementation|for now|out))\b|\bfill (this|it) in\b`),
		"marked as unfinished or a placeholder: finish it"},
	{"demo-shortcut", rvWarn, rx(`(?i)\bin a real (app|application|implementation|system|world|project|scenario)\b|\bfor (demo|demonstration|illustration|example|testing) purposes( only)?\b|\b(simplified|simple|naive|basic) (implementation|version|approach) for now\b|\b(hard-?coded|mock|dummy|fake|sample) (data|value|values|response|implementation|key|token|user) (for now|here)\b|\bfor now,? (we|just|i)\b|\bthis would normally\b|\bwould need to be (replaced|implemented)\b`),
		"a shortcut the comment itself says isn't real: do it properly or explain why it's fine"},
	{"llm-comment", rvWarn, rx(`(?i)\bas an ai\b|\bi (have|'ve) (now )?(updated|added|modified|fixed|changed|rewritten)\b|\bhere('s| is) (the|your) (updated|fixed|new|modified|corrected|complete)\b|\blet me know if\b|\bhope this helps\b|\bi apologi[sz]e\b`),
		"chat text in a comment: rewrite it as a normal code comment"},
}

var rvTodo = rx(`\b(TODO|FIXME|XXX|HACK)\b`)

// Stubs that throw or panic instead of doing the work.
var rvStubRules = map[string][]*regexp.Regexp{
	"go":    {rx(`\bpanic\(\s*"(?i:[^"]*\b(not (yet )?implemented|unimplemented|todo|implement me)\b)`), rx(`\b(errors\.New|fmt\.Errorf)\(\s*"(?i:not (yet )?implemented)`)},
	"js":    {rx(`\bthrow\s+new\s+Error\(\s*["'` + "`" + `](?i:[^"'` + "`" + `]*\b(not (yet )?implemented|unimplemented|todo|implement me)\b)`)},
	"kt":    {rx(`(^|[^\w.])TODO\(\s*("|\))`)},
	"java":  {rx(`\bthrow\s+new\s+UnsupportedOperationException\(\s*"(?i:[^"]*\b(not (yet )?implemented|todo|implement me)\b)`)},
	"rs":    {rx(`\b(todo|unimplemented)!\(`)},
	"cs":    {rx(`\bthrow\s+new\s+NotImplementedException\(`)},
	"swift": {rx(`\bfatalError\(\s*"(?i:[^"]*\b(not (yet )?implemented|unimplemented|todo)\b)`)},
	"dart":  {rx(`\bthrow\s+UnimplementedError\(`)},
	"php":   {rx(`\bthrow\s+new\s+\\?(Exception|LogicException|BadMethodCallException)\(\s*['"](?i:[^'"]*\b(not (yet )?implemented|todo)\b)`)},
}

func init() { rvStubRules["ts"] = rvStubRules["js"] }

var rvSuppress = map[string]*regexp.Regexp{
	"py":    rx(`#\s*(noqa\b|type:\s*ignore|pyright:\s*ignore|nosec\b|pragma:\s*no\s*cover|pylint:\s*disable|mypy:\s*ignore)`),
	"js":    rx(`eslint-disable|@ts-(ignore|nocheck|expect-error)\b|istanbul ignore|c8 ignore`),
	"go":    rx(`//\s*(nolint|lint:ignore|#?nosec)\b|#nosec\b`),
	"kt":    rx(`@(file:)?Suppress(Warnings|Lint)?\(|//\s*noinspection\b`),
	"java":  rx(`@Suppress(Warnings|Lint)?\(|//\s*noinspection\b`),
	"rs":    rx(`#!?\[allow\(`),
	"sh":    rx(`#\s*shellcheck\s+disable=`),
	"ps1":   rx(`SuppressMessage(Attribute)?\(`),
	"cs":    rx(`#pragma\s+warning\s+disable|SuppressMessage\(`),
	"swift": rx(`//\s*swiftlint:disable`),
	"dart":  rx(`//\s*ignore(_for_file)?:`),
	"php":   rx(`@phpstan-ignore|@psalm-suppress|phpcs:ignore`),
}

func init() { rvSuppress["ts"] = rvSuppress["js"] }

var rvDebug = map[string]*regexp.Regexp{
	"py":   rx(`^\s*(breakpoint\(\)|import i?pdb\b|i?pdb\.set_trace\(\)|from i?pdb import)`),
	"js":   rx(`^\s*debugger\s*;?\s*$`),
	"kt":   rx(`\.printStackTrace\(\)`),
	"java": rx(`\.printStackTrace\(\)`),
	"rs":   rx(`\bdbg!\(`),
	"php":  rx(`\b(var_dump|dd|print_r)\(`),
}

func init() { rvDebug["ts"] = rvDebug["js"] }

var rvSkipRules = map[string]*regexp.Regexp{
	"py":    rx(`@pytest\.mark\.(skip|xfail)\b|@unittest\.skip|\bpytest\.skip\(|\bself\.skipTest\(`),
	"go":    rx(`\bt\.Skip(Now|f)?\(`),
	"js":    rx(`\b(it|test|describe|context)\.(skip|todo)\(|\bx(it|describe|test)\(`),
	"kt":    rx(`@(Ignore|Disabled)\b`),
	"java":  rx(`@(Ignore|Disabled)\b`),
	"rs":    rx(`#\[ignore\b`),
	"cs":    rx(`\[(Fact|Theory)\(\s*Skip\s*=|\[Ignore\b`),
	"swift": rx(`\bXCTSkip\(`),
	"dart":  rx(`\bskip:\s*(true|['"])`),
}

var rvFocus = rx(`\b(it|test|describe|context)\.only\(|\bf(it|describe)\(`)

func init() { rvSkipRules["ts"] = rvSkipRules["js"] }

var (
	rvFence       = rx("^\\s*```")
	rvProse       = rx(`(?i)^\s*(here('s| is) (the|an?|your) (updated|complete|full|fixed|corrected|modified|final|revised|new)\b|certainly[!,]|sure[,!] here|i('ve| have) (updated|added|fixed|made)\b|let me know if\b)`)
	rvConflict    = rx(`^(<{7}|>{7})( |$)|^={7}$`)
	rvPlaceholder = rx(`(?i)["'](your[_ -]?(api[_ -]?)?(key|token|secret|password)([_ -]?here)?|<\s*your[^>]*>|replace[_ -]?me|changeme|change[_ -]me|insert[_ -]?(your[_ -]?)?(key|token)[_ -]?here|xxxx+|todo)["']`)
	rvUserPath    = rx(`(?i)\b[a-z]:\\{1,2}(users|documents and settings)\\{1,2}[^\\/"'\s]+|/home/[a-z_][\w.-]*/|/Users/[A-Za-z][\w.-]*/`)
	rvGoDiscard   = rx(`^\s*_\s*=\s*err\s*$`)
	rvEmptyCatch  = rx(`\bcatch\s*\([^)]*\)\s*\{\s*\}`)
	rvCatchOpen   = rx(`\bcatch\s*\([^)]*\)\s*\{\s*$`)
	rvPyExcept    = rx(`^\s*except\b[^:]*:\s*(pass|\.\.\.)?\s*$`)
	rvPyPass      = rx(`^\s*(pass|\.\.\.)\s*$`)
)

var rvHashLangs = map[string]bool{"py": true, "sh": true, "ps1": true, "rb": true}

// Languages whose suppressions are annotations in the code rather than comments.
var rvAnnoSuppress = map[string]bool{"kt": true, "java": true, "rs": true, "cs": true, "ps1": true}

var (
	rvQuotedText = regexp.MustCompile("\"[^\"]*\"|`[^`]*`")
	rvRegexy     = regexp.MustCompile(`\b(MustCompile|Compile|compile|RegExp|Regex|toRegex|rx|match|search|fullmatch|sub|findall)\s*\(|\(\?[imsU]+\)`)
	rvCallSkip   = regexp.MustCompile(`\bt\.Skip(Now|f)?\(|\bpytest\.skip\(|\bself\.skipTest\(`)
)

// rvUnconditional says whether a skip on line i switches a test off for good: decorators and
// .skip() forms always do; t.Skip()/pytest.skip() only when not inside an if (e.g. "no git here").
func rvUnconditional(lang string, lines []string, i int) bool {
	if !rvCallSkip.MatchString(lines[i]) {
		return true
	}
	ind := rvIndent(lines[i])
	for k := i - 1; k >= 0; k-- {
		t := strings.TrimSpace(lines[k])
		if t == "" {
			continue
		}
		if rvIndent(lines[k]) < ind {
			return strings.HasPrefix(t, "func ") || strings.HasPrefix(t, "def ") || strings.HasPrefix(t, "async def ")
		}
	}
	return true
}

// rvSplitComment separates a line into code and comment text (a rough lexer: it knows quotes
// and the comment markers of each language, not every string syntax).
func rvSplitComment(lang, s string) (string, string) {
	t := strings.TrimSpace(s)
	hash := rvHashLangs[lang]
	if !hash && (strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "* ") || t == "*" || strings.HasPrefix(t, "*/")) {
		return "", strings.TrimLeft(t, "/* ")
	}
	if lang == "lua" {
		if i := strings.Index(s, "--"); i >= 0 {
			return s[:i], s[i+2:]
		}
		return s, ""
	}
	var q byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if q != 0 {
			if ch == '\\' {
				i++
			} else if ch == q {
				q = 0
			}
			continue
		}
		switch ch {
		case '"', '`':
			q = ch
		case '\'':
			if lang != "rs" {
				q = ch
			}
		case '#':
			if hash || lang == "php" {
				return s[:i], s[i+1:]
			}
		case '/':
			if !hash && i+1 < len(s) && (s[i+1] == '/' || s[i+1] == '*') {
				return s[:i], s[i+2:]
			}
		}
	}
	return s, ""
}

var rvMultiDelims = map[string][]string{"go": {"`"}, "js": {"`"}, "ts": {"`"}, "py": {`"""`, `'''`},
	"kt": {`"""`}, "java": {`"""`}, "swift": {`"""`}, "scala": {`"""`}, "dart": {`"""`, `'''`}, "cs": {`"""`}}

// rvStringMask marks the lines that begin inside a multi-line string (Go/JS backtick strings,
// Python/Kotlin triple quotes...), so text and code samples inside strings aren't taken for code.
func rvStringMask(lang string, lines []string) []bool {
	delims := rvMultiDelims[lang]
	mask := make([]bool, len(lines))
	if len(delims) == 0 {
		return mask
	}
	open := ""
	for i, l := range lines {
		mask[i] = open != ""
		k := 0
		for k < len(l) {
			if open != "" {
				j := strings.Index(l[k:], open)
				if j < 0 {
					break
				}
				k += j + len(open)
				open = ""
				continue
			}
			hit := ""
			for _, d := range delims {
				if strings.HasPrefix(l[k:], d) {
					hit = d
					break
				}
			}
			ch := l[k]
			switch {
			case hit != "":
				open = hit
				k += len(hit)
			case ch == '"' || (ch == '\'' && lang != "rs"):
				q := ch
				k++
				for k < len(l) && l[k] != q {
					if l[k] == '\\' {
						k++
					}
					k++
				}
				k++
			case ch == '#' && lang == "py":
				k = len(l)
			case ch == '/' && k+1 < len(l) && l[k+1] == '/' && lang != "py":
				k = len(l)
			default:
				k++
			}
		}
	}
	return mask
}

func rvNewTodo(c *rvCtx) int {
	if c.diff == nil {
		return rvNote
	}
	return rvWarn
}

func rvSlop(c *rvCtx) rvResult {
	var r rvResult
	r.ran = append(r.ran, "slop checks")
	if c.diff != nil {
		for _, k := range c.diff.Order {
			fd := c.diff.Files[k]
			rvSlopFile(c, &r, fd.Path, fd)
		}
		rvTestIntegrity(c, &r)
	} else {
		n := 0
		for _, f := range c.inv.files {
			if rvLang(f) == "" && !rvTextish(f) {
				continue
			}
			if n++; n > 6000 {
				r.notes = append(r.notes, "slop checks: stopped after 6000 files")
				break
			}
			rvSlopFile(c, &r, f, nil)
		}
	}
	rvDupBlocks(c, &r)
	rvAddedFiles(c, &r)
	return r
}

func rvTextish(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".md", ".txt", ".json", ".yml", ".yaml", ".toml", ".xml", ".gradle", ".cfg", ".ini", ".html", ".css", ".scss", ".vue", ".svelte", ".sql", ".properties":
		return true
	}
	return false
}

func rvGenerated(lines []string, rel string) bool {
	low := strings.ToLower(rel)
	if strings.Contains(low, "generated") || strings.HasSuffix(low, ".pb.go") || strings.HasSuffix(low, ".g.dart") || strings.HasSuffix(low, ".min.js") {
		return true
	}
	for i := 0; i < len(lines) && i < 5; i++ {
		if strings.Contains(lines[i], "Code generated") || strings.Contains(lines[i], "DO NOT EDIT") || strings.Contains(lines[i], "@generated") {
			return true
		}
	}
	return false
}

func rvSlopFile(c *rvCtx, r *rvResult, rel string, fd *rvFileDiff) {
	lines, ok := rvReadLines(c.abs(rel))
	if !ok || rvGenerated(lines, rel) {
		return
	}
	lang := rvLang(rel)
	isTest := rvIsTest(rel)
	mask := rvStringMask(lang, lines)
	check := func(n int) bool { return fd == nil || fd.Lines[n] }
	add := func(n int, code string, sev int, msg string) {
		r.add(rvIssue{Tool: "slop", File: rel, Line: n, Sev: sev, Code: code, Msg: msg})
	}
	for i, l := range lines {
		n := i + 1
		if !check(n) {
			continue
		}
		if rvConflict.MatchString(l) {
			add(n, "conflict-marker", rvErr, "merge conflict marker left in the file")
			continue
		}
		if lang == "" || mask[i] {
			continue
		}
		jsLike := lang
		if lang == "ts" {
			jsLike = "js"
		}
		code, comment := rvSplitComment(lang, l)
		if rvFence.MatchString(l) {
			add(n, "llm-text", rvWarn, "a markdown ``` fence in source code: chat output pasted into the file? (ignore if it's inside a string)")
		} else if strings.TrimSpace(code) != "" && rvProse.MatchString(code) {
			add(n, "llm-text", rvWarn, "chat text pasted into code: remove it")
		}
		if comment != "" {
			matched := false
			bare := rvQuotedText.ReplaceAllString(comment, "")
			for _, rule := range rvCommentRules {
				if rule.re.MatchString(bare) {
					sev := rule.sev
					if isTest && sev == rvErr && rule.code == "unfinished" {
						sev = rvWarn
					}
					add(n, rule.code, sev, rule.msg)
					matched = true
					break
				}
			}
			if !matched && rvTodo.MatchString(bare) {
				add(n, "todo", rvNewTodo(c), "TODO/FIXME left in: finish it now, or keep it only if it's tracked work")
			}
		}
		if re := rvSuppress[lang]; re != nil {
			marker := "//"
			if rvHashLangs[lang] {
				marker = "#"
			}
			if (comment != "" && re.MatchString(marker+comment)) || (rvAnnoSuppress[lang] && re.MatchString(rvQuotedText.ReplaceAllString(code, `""`))) {
				add(n, "suppression", rvNewTodo(c), "new lint/type-check suppression: fix the cause instead, or say in the comment why it's safe")
			}
		}
		if strings.TrimSpace(code) == "" {
			continue
		}
		for _, re := range rvStubRules[lang] {
			if re.MatchString(code) {
				sev := rvErr
				if isTest {
					sev = rvWarn
				}
				add(n, "stub", sev, "throws/panics \"not implemented\" instead of doing the work")
				break
			}
		}
		if re := rvDebug[lang]; re != nil && re.MatchString(code) {
			add(n, "debug-leftover", rvWarn, "debugging leftover: remove it (or log properly)")
		}
		if isTest || lang == "go" || jsLike == "js" {
			if re := rvSkipRules[lang]; re != nil && re.MatchString(code) && (isTest || lang != "go") && rvUnconditional(lang, lines, i) {
				add(n, "test-skipped", rvWarn, "a test is skipped: fix the test or the code, don't switch it off")
			}
			if jsLike == "js" && rvFocus.MatchString(code) {
				add(n, "test-focused", rvErr, ".only/fit/fdescribe: every other test in the file is silently skipped")
			}
		}
		if isTest {
			if why := rvTrivialAssert(lang, code); why != "" {
				add(n, "trivial-assert", rvErr, why)
			}
		} else if !rvRegexy.MatchString(code) {
			if rvPlaceholder.MatchString(code) {
				add(n, "placeholder-value", rvWarn, "placeholder value left in the code (like your-api-key or changeme): use the real value or a setting")
			}
			if m := rvUserPath.FindString(code); m != "" && !strings.Contains(strings.ToLower(m), "runner") {
				add(n, "hardcoded-path", rvWarn, "hard-coded personal path ("+strings.TrimRight(m, `\/`)+"): breaks on any other machine")
			}
		}
		switch {
		case lang == "go" && rvGoDiscard.MatchString(code):
			add(n, "swallowed-error", rvWarn, "error thrown away with _ = err: handle it or return it")
		case (lang == "kt" || lang == "java" || lang == "cs" || lang == "dart" || lang == "php") && (rvEmptyCatch.MatchString(code) || (rvCatchOpen.MatchString(code) && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) == "}")):
			add(n, "swallowed-error", rvWarn, "empty catch: the error disappears silently; handle or log it")
		case lang == "py" && !c.lintPy && rvPyExcept.MatchString(code):
			m := rvPyExcept.FindStringSubmatch(code)
			if m[1] != "" || (i+1 < len(lines) && rvPyPass.MatchString(lines[i+1])) {
				add(n, "swallowed-error", rvWarn, "except … pass: the error disappears silently; handle or log it")
			}
		}
	}
	if lang == "py" && !isTest && !strings.HasSuffix(rel, ".pyi") {
		rvPyStubs(lines, mask, check, func(n, end int, code string, sev int, msg string) {
			r.add(rvIssue{Tool: "slop", File: rel, Line: n, end: end, Sev: sev, Code: code, Msg: msg})
		})
	}
	if isTest || (lang == "go" && strings.HasSuffix(rel, "_test.go")) {
		for _, t := range rvFindTests(lang, lines) {
			if mask[t.start-1] || (fd != nil && !rvRangeChanged(fd, t.start, t.end)) {
				continue
			}
			if !rvAsserts(lang, strings.Join(lines[t.start-1:t.end], "\n")) {
				r.add(rvIssue{Tool: "slop", File: rel, Line: t.start, end: t.end, Sev: rvErr, Code: "hollow-test",
					Msg: t.name + " has no assertion: it passes whatever the code does. Check the result"})
			}
		}
	}
	if rvBraceLangs[lang] && (lang != "kt" || !c.inv.droid) {
		var code []string
		for i, l := range lines {
			if !mask[i] {
				code = append(code, l)
			}
		}
		if open, closed := rvBraces(lang, code); open != closed {
			r.add(rvIssue{Tool: "slop", File: rel, Line: len(lines), Sev: rvWarn, Code: "unbalanced-braces",
				Msg: fmt.Sprintf("braces don't balance ({ %d, } %d): the file may be cut off or broken", open, closed)})
		}
	}
}

var rvBraceLangs = map[string]bool{"kt": true, "java": true, "cs": true, "swift": true, "c": true, "rs": true, "dart": true, "php": true, "scala": true}

func rvBraces(lang string, lines []string) (int, int) {
	open, closed := 0, 0
	inBlock := false
	for _, l := range lines {
		if inBlock {
			i := strings.Index(l, "*/")
			if i < 0 {
				continue
			}
			l, inBlock = l[i+2:], false
		}
		var q byte
		for i := 0; i < len(l); i++ {
			ch := l[i]
			if q != 0 {
				if ch == '\\' {
					i++
				} else if ch == q {
					q = 0
				}
				continue
			}
			switch {
			case ch == '"' || (ch == '\'' && lang != "rs"):
				q = ch
			case ch == '/' && i+1 < len(l) && l[i+1] == '/':
				i = len(l)
			case ch == '/' && i+1 < len(l) && l[i+1] == '*':
				if j := strings.Index(l[i+2:], "*/"); j >= 0 {
					i += j + 3
				} else {
					inBlock, i = true, len(l)
				}
			case ch == '{':
				open++
			case ch == '}':
				closed++
			}
		}
	}
	return open, closed
}

func rvRangeChanged(fd *rvFileDiff, a, b int) bool {
	if fd.New {
		return true
	}
	for n := a; n <= b; n++ {
		if fd.Lines[n] {
			return true
		}
	}
	return false
}

func rvIndent(s string) int {
	n := 0
	for _, ch := range s {
		switch ch {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

var (
	rvPyDef   = rx(`^(\s*)(?:async\s+)?def\s+(\w+)\s*\(`)
	rvPyClass = rx(`^(\s*)class\s+\w+\s*(?:\((.*)\))?\s*:`)
)

// rvPyStubs reports Python functions whose whole body is pass/... or raise NotImplementedError,
// except abstract methods, overloads, Protocol/ABC classes and dunder methods.
func rvPyStubs(lines []string, mask []bool, check func(int) bool, add func(n, end int, code string, sev int, msg string)) {
	for i, l := range lines {
		m := rvPyDef.FindStringSubmatch(l)
		if m == nil || mask[i] {
			continue
		}
		ind, name := rvIndent(m[1]), m[2]
		if strings.HasPrefix(name, "__") && strings.HasSuffix(name, "__") {
			continue
		}
		skip := false
		for k := i - 1; k >= 0 && k >= i-6; k-- {
			t := strings.TrimSpace(lines[k])
			if !strings.HasPrefix(t, "@") {
				break
			}
			if strings.Contains(t, "abstractmethod") || strings.Contains(t, "overload") || strings.Contains(t, "abstractproperty") {
				skip = true
			}
		}
		for k := i - 1; k >= 0 && !skip; k-- {
			if cm := rvPyClass.FindStringSubmatch(lines[k]); cm != nil && rvIndent(cm[1]) < ind {
				bases := cm[2]
				if strings.Contains(bases, "Protocol") || strings.Contains(bases, "ABC") || strings.Contains(bases, "Interface") || strings.Contains(bases, "metaclass") {
					skip = true
				}
				break
			}
			if t := strings.TrimSpace(lines[k]); t != "" && rvIndent(lines[k]) < ind && !strings.HasPrefix(t, "@") && !strings.HasPrefix(t, "#") {
				if !strings.HasPrefix(t, "class ") {
					break
				}
			}
		}
		if skip {
			continue
		}
		// the def line may continue over several lines up to the colon
		j := i
		for j < len(lines) && !strings.HasSuffix(strings.TrimSpace(strings.SplitN(lines[j], "#", 2)[0]), ":") && j < i+15 {
			j++
		}
		var stmts []string
		end := j
		inDoc := ""
		for k := j + 1; k < len(lines); k++ {
			t := strings.TrimSpace(lines[k])
			if t == "" {
				continue
			}
			if inDoc == "" && rvIndent(lines[k]) <= ind {
				break
			}
			end = k
			if inDoc != "" {
				if strings.Contains(t, inDoc) {
					inDoc = ""
				}
				continue
			}
			if strings.HasPrefix(t, "#") {
				continue
			}
			if len(stmts) == 0 && (strings.HasPrefix(t, `"""`) || strings.HasPrefix(t, `'''`) || strings.HasPrefix(t, `r"""`)) {
				q := `"""`
				if strings.HasPrefix(t, `'''`) {
					q = `'''`
				}
				rest := strings.TrimPrefix(strings.TrimPrefix(t, "r"), q)
				if !strings.Contains(rest, q) {
					inDoc = q
				}
				continue
			}
			stmts = append(stmts, t)
			if len(stmts) > 1 {
				break
			}
		}
		if len(stmts) != 1 {
			continue
		}
		touched := false
		for n := i + 1; n <= end+1; n++ {
			if check(n) {
				touched = true
				break
			}
		}
		if !touched {
			continue
		}
		s := stmts[0]
		switch {
		case strings.HasPrefix(s, "raise NotImplementedError") || strings.HasPrefix(s, "raise NotImplemented"):
			add(i+1, end+1, "stub", rvErr, name+"() only raises NotImplementedError: it isn't written yet")
		case s == "pass" || s == "...":
			add(i+1, end+1, "stub", rvWarn, name+"() does nothing (its body is just "+s+"): unfinished?")
		}
	}
}

type rvTestFn struct {
	name       string
	start, end int
}

var (
	rvGoTest   = rx(`^func\s+(Test\w*)\s*\(\s*\w+\s+\*testing\.T\s*\)`)
	rvJsTest   = rx(`^\s*(?:it|test)(?:\.(?:concurrent|each\(.*?\)|failing))?\s*\(\s*(['"` + "`" + `])(.*?)['"` + "`" + `]`)
	rvAnnoTest = rx(`^\s*(@(Test|ParameterizedTest|RepeatedTest)\b|#\[(tokio::)?test\b|\[(Fact|Theory|Test|TestMethod|TestCase)\b)`)
	rvFnName   = rx(`\b(?:fun|fn|void|func|Task|async\s+Task)\s+(\w+)\s*\(`)
	rvSwiftDef = rx(`^\s*func\s+(test\w*)\s*\(`)
	rvDartTest = rx(`^\s*(?:test|testWidgets)\s*\(\s*['"](.*?)['"]`)
)

// rvBraceEnd returns the 1-based line where the block opened at or after line start closes.
func rvBraceEnd(lang string, lines []string, start int) int {
	depth, opened := 0, false
	for k := start; k <= len(lines) && k < start+3000; k++ {
		o, cl := rvBraces(lang, []string{lines[k-1]})
		if o > 0 {
			opened = true
		}
		depth += o - cl
		if opened && depth <= 0 {
			return k
		}
		if !opened && k > start+6 {
			return start
		}
	}
	return len(lines)
}

func rvFindTests(lang string, lines []string) []rvTestFn {
	var out []rvTestFn
	switch lang {
	case "go":
		for i, l := range lines {
			if m := rvGoTest.FindStringSubmatch(l); m != nil {
				out = append(out, rvTestFn{m[1], i + 1, rvBraceEnd(lang, lines, i+1)})
			}
		}
	case "py":
		for i, l := range lines {
			m := rvPyDef.FindStringSubmatch(l)
			if m == nil || !strings.HasPrefix(m[2], "test") {
				continue
			}
			ind, end := rvIndent(m[1]), i+1
			for k := i + 1; k < len(lines); k++ {
				if strings.TrimSpace(lines[k]) == "" {
					continue
				}
				if rvIndent(lines[k]) <= ind {
					break
				}
				end = k + 1
			}
			out = append(out, rvTestFn{m[2], i + 1, end})
		}
	case "js", "ts":
		for i, l := range lines {
			if m := rvJsTest.FindStringSubmatch(l); m != nil {
				out = append(out, rvTestFn{"test \"" + m[2] + "\"", i + 1, rvBraceEnd("js", lines, i+1)})
			}
		}
	case "dart":
		for i, l := range lines {
			if m := rvDartTest.FindStringSubmatch(l); m != nil {
				out = append(out, rvTestFn{"test \"" + m[1] + "\"", i + 1, rvBraceEnd(lang, lines, i+1)})
			}
		}
	case "swift":
		for i, l := range lines {
			if m := rvSwiftDef.FindStringSubmatch(l); m != nil {
				out = append(out, rvTestFn{m[1], i + 1, rvBraceEnd(lang, lines, i+1)})
			}
		}
	case "kt", "java", "rs", "cs":
		for i, l := range lines {
			if !rvAnnoTest.MatchString(l) {
				continue
			}
			for k := i; k < len(lines) && k < i+8; k++ {
				if m := rvFnName.FindStringSubmatch(lines[k]); m != nil {
					out = append(out, rvTestFn{m[1], k + 1, rvBraceEnd(lang, lines, k+1)})
					break
				}
			}
		}
	}
	return out
}

var rvAssertRe = map[string]*regexp.Regexp{
	"go":    rx(`\b\w+\.(Error|Errorf|Fatal|Fatalf|Fail|FailNow)\(|\b(assert|require|is|qt|check|expect|must)\.\w+\(|\bcmp\.(Diff|Equal)\(|\w+\(\s*t\s*[,)]|\bpanic\(`),
	"py":    rx(`\bassert\b|self\.(assert\w*|fail)\(|\bpytest\.(raises|warns|fail|approx)\b|\.assert_\w+\(|\b(assert|check|verify|expect)_?\w*\(`),
	"js":    rx(`\bexpect\s*[(.]|\bassert\w*\s*[.(]|\.should\b|\bt\.(is|not|deepEqual|true|false|throws|snapshot|assert|equal)\(|toMatch\w*Snapshot|\bexpectTypeOf\b|\b(verify|check)\w*\(`),
	"kt":    rx(`\bassert\w*\s*[(<{]|\bAssert\.|\b(co)?[vV]erify\w*\s*[({]|\bexpect\w*\s*[({]|\bshould\w*\b|\.check\(|\bfail\(|\.assert\w+\(|\bcheck\w*\(`),
	"rs":    rx(`\b(assert|assert_eq|assert_ne|debug_assert\w*|panic|assert_matches)!|\.unwrap\(\)|\.expect\(|\?\s*;`),
	"cs":    rx(`\bAssert\.|\.Should\(|\bVerify\(|\bAssert\w*\(|\.Received\(`),
	"swift": rx(`\bXCTAssert\w*\(|\bXCTFail\(|#expect\(|#require\(|\bXCTUnwrap\(`),
	"dart":  rx(`\bexpect\w*\(|\bverify\w*\(`),
}

func init() {
	rvAssertRe["ts"] = rvAssertRe["js"]
	rvAssertRe["java"] = rvAssertRe["kt"]
}

func rvAsserts(lang, body string) bool {
	re := rvAssertRe[lang]
	if re == nil {
		return true
	}
	// the test's own name line doesn't count (e.g. "def test_assert_x")
	if i := strings.Index(body, "\n"); i >= 0 {
		body = body[i+1:]
	} else {
		return true
	}
	return re.MatchString(body)
}

var (
	rvTrivPy   = rx(`^\s*(?:assert|self\.assertTrue)\s*\(?\s*(True|1|not\s+False)\s*\)?\s*(,.*)?$`)
	rvTrivEq   = rx(`^\s*assert\s+([\w.]+)\s*==\s*([\w.]+)\s*(,.*)?$`)
	rvTrivJs   = rx(`expect\(\s*(true|false|1|0|null|undefined)\s*\)\.(toBe|toEqual|toStrictEqual)\(\s*(true|false|1|0|null|undefined)\s*\)`)
	rvTrivKt   = rx(`\bassert(True|False)\(\s*(true|false)\s*\)`)
	rvTrivKtEq = rx(`\bassertEquals\(\s*([\w.]+)\s*,\s*([\w.]+)\s*\)`)
	rvTrivRs   = rx(`\bassert!\(\s*true\s*\)|\bassert_eq!\(\s*([\w.]+)\s*,\s*([\w.]+)\s*\)`)
)

// rvTrivialAssert spots assertions that can never fail.
func rvTrivialAssert(lang, code string) string {
	const why = "this assertion can never fail: assert the real result"
	switch lang {
	case "py":
		if rvTrivPy.MatchString(code) {
			return why
		}
		if m := rvTrivEq.FindStringSubmatch(code); m != nil && m[1] == m[2] {
			return why
		}
	case "js", "ts":
		if m := rvTrivJs.FindStringSubmatch(code); m != nil && m[1] == m[3] {
			return why
		}
	case "kt", "java":
		if m := rvTrivKt.FindStringSubmatch(code); m != nil && strings.EqualFold(m[1], m[2]) {
			return why
		}
		if m := rvTrivKtEq.FindStringSubmatch(code); m != nil && m[1] == m[2] {
			return why
		}
	case "rs":
		if m := rvTrivRs.FindStringSubmatch(code); m != nil && (m[1] == "" || m[1] == m[2]) {
			return why
		}
	}
	return ""
}

var rvTestDef = map[string]*regexp.Regexp{
	"py":   rx(`^\s*(async\s+)?def\s+test\w*\s*\(`),
	"go":   rx(`^\s*func\s+Test\w*\(`),
	"js":   rx(`^\s*(it|test)(\.\w+)?\s*\(`),
	"kt":   rx(`@Test\b`),
	"java": rx(`@Test\b`),
	"rs":   rx(`#\[(tokio::)?test\b`),
	"cs":   rx(`\[(Fact|Theory|Test|TestMethod)\b`),
}

func init() { rvTestDef["ts"] = rvTestDef["js"] }

// rvTestIntegrity compares what a change removed from tests with what it added.
func rvTestIntegrity(c *rvCtx, r *rvResult) {
	testsTouched, srcFiles, srcLines := false, 0, 0
	for _, k := range c.diff.Order {
		fd := c.diff.Files[k]
		lang := rvLang(fd.Path)
		if lang == "" {
			continue
		}
		if !rvIsTest(fd.Path) {
			if len(fd.Added) > 0 {
				srcFiles++
				srcLines += len(fd.Added)
			}
			continue
		}
		testsTouched = true
		count := func(re *regexp.Regexp, ls []string) int {
			n := 0
			for _, l := range ls {
				if code, _ := rvSplitComment(lang, l); re.MatchString(code) {
					n++
				}
			}
			return n
		}
		added := make([]string, len(fd.Added))
		for i, a := range fd.Added {
			added[i] = a.T
		}
		first := 1
		if len(fd.Added) > 0 {
			first = fd.Added[0].N
		}
		if re := rvTestDef[lang]; re != nil {
			if rm, ad := count(re, fd.Removed), count(re, added); rm > ad {
				r.add(rvIssue{Tool: "slop", File: fd.Path, Line: first, Sev: rvWarn, Code: "tests-removed", scope: rvScopeFile,
					Msg: fmt.Sprintf("%d test(s) removed and %d added: make sure nothing that should be tested lost its test", rm, ad)})
			}
		}
		if re := rvAssertRe[lang]; re != nil {
			if rm, ad := count(re, fd.Removed), count(re, added); rm >= 2 && rm > ad*2 {
				r.add(rvIssue{Tool: "slop", File: fd.Path, Line: first, Sev: rvWarn, Code: "asserts-removed", scope: rvScopeFile,
					Msg: fmt.Sprintf("%d assertion line(s) removed, %d added: were tests weakened to make them pass?", rm, ad)})
			}
		}
	}
	for _, p := range c.diff.Deleted {
		if rvLang(p) != "" && rvIsTest(p) {
			testsTouched = true
			r.add(rvIssue{Tool: "slop", File: p, Sev: rvWarn, Code: "test-file-deleted", scope: rvScopeAlways,
				Msg: "a test file was deleted: make sure what it tested is still tested"})
		}
	}
	if !testsTouched && srcFiles > 0 && srcLines >= 15 {
		r.add(rvIssue{Tool: "slop", Sev: rvNote, Code: "no-tests", scope: rvScopeAlways,
			Msg: fmt.Sprintf("%d source file(s) changed (+%d lines) but no test was added or changed", srcFiles, srcLines)})
	}
}

// ---- copied blocks ----

const rvDupWin = 8

type rvSig struct {
	n int
	t string
}

var rvSigSkip = rx(`^(import\b|from\s+\S+\s+import\b|package\b|#include\b|using\b|use\s|require\(|@)`)

var rvLoneString = regexp.MustCompile("^(\"[^\"]*\"|'[^']*'|`[^`]*`|\\w+ \"[^\"]*\"),?$")

func rvSigLines(lang string, lines []string) []rvSig {
	var out []rvSig
	mask := rvStringMask(lang, lines)
	block := ""
	for i, l := range lines {
		if mask[i] {
			continue
		}
		code, _ := rvSplitComment(lang, l)
		t := strings.Join(strings.Fields(code), " ")
		if block != "" {
			if strings.Contains(t, block) {
				block = ""
			}
			continue
		}
		switch {
		case t == "import (" || (strings.HasPrefix(t, "from ") && strings.HasSuffix(t, "import (")):
			block = ")"
			continue
		case strings.HasPrefix(t, "import {") && !strings.Contains(t, "}"):
			block = "}"
			continue
		}
		if len(t) < 4 || strings.Trim(t, "{}()[];,:") == "" || rvSigSkip.MatchString(t) || rvLoneString.MatchString(t) {
			continue
		}
		out = append(out, rvSig{i + 1, t})
	}
	return out
}

func rvHashWin(s []rvSig) uint64 {
	h := fnv.New64a()
	for _, x := range s {
		h.Write([]byte(x.t))
		h.Write([]byte{'\n'})
	}
	return h.Sum64()
}

func rvDupBlocks(c *rvCtx, r *rvResult) {
	type loc struct {
		file string
		line int
	}
	langs := map[string]bool{}
	var targets []string
	if c.diff != nil {
		for _, k := range c.diff.Order {
			fd := c.diff.Files[k]
			if l := rvLang(fd.Path); l != "" && !rvIsTest(fd.Path) && len(fd.Added) >= rvDupWin {
				langs[l] = true
				targets = append(targets, fd.Path)
			}
		}
	} else {
		for l, fs := range c.inv.byLang {
			langs[l] = true
			for _, f := range fs {
				if !rvIsTest(f) {
					targets = append(targets, f)
				}
			}
		}
	}
	if len(targets) == 0 {
		return
	}
	index := map[uint64][]loc{}
	sigs := map[string][]rvSig{}
	total := 0
	for l := range langs {
		for _, f := range c.inv.byLang[l] {
			if rvIsTest(f) || total > 400000 {
				continue
			}
			lines, ok := rvReadLines(c.abs(f))
			if !ok || rvGenerated(lines, f) {
				continue
			}
			s := rvSigLines(l, lines)
			sigs[f] = s
			total += len(s)
			for i := 0; i+rvDupWin <= len(s); i++ {
				h := rvHashWin(s[i : i+rvDupWin])
				index[h] = append(index[h], loc{f, s[i].n})
			}
		}
	}
	reported := 0
	seenPair := map[string]bool{}
	for _, f := range targets {
		s := sigs[f]
		var fd *rvFileDiff
		if c.diff != nil {
			fd = c.diff.Files[rvKey(f)]
		}
		for i := 0; i+rvDupWin <= len(s); i++ {
			if fd != nil {
				all := true
				for _, x := range s[i : i+rvDupWin] {
					if !fd.Lines[x.n] {
						all = false
						break
					}
				}
				if !all {
					continue
				}
			}
			var other *loc
			for _, o := range index[rvHashWin(s[i:i+rvDupWin])] {
				if o.file == f && o.line == s[i].n {
					continue
				}
				o := o
				other = &o
				break
			}
			if other == nil {
				continue
			}
			pair := f + "|" + other.file
			if other.file < f {
				pair = other.file + "|" + f
			}
			if c.diff == nil && seenPair[pair] {
				i += rvDupWin - 1
				continue
			}
			seenPair[pair] = true
			r.add(rvIssue{Tool: "slop", File: f, Line: s[i].n, end: s[i+rvDupWin-1].n, Sev: rvWarn, Code: "copied-block",
				Msg: fmt.Sprintf("these %d+ lines are copied from %s:%d: share one function instead", rvDupWin, other.file, other.line)})
			reported++
			i += rvDupWin - 1
			if reported >= 15 {
				return
			}
		}
	}
}

// ---- files added to the repo ----

var rvSecretName = rx(`(?i)(^|/)(\.env(\.[\w-]+)?|id_(rsa|dsa|ecdsa|ed25519)|[^/]*\.(pem|key|p12|pfx|jks|keystore|ovpn|kdbx)|credentials\.json|service[-_]?account[^/]*\.json|\.npmrc|\.pypirc|secrets?\.(json|ya?ml|toml))$`)
var rvEnvSample = rx(`(?i)\.env\.(example|sample|template|dist|defaults?)$`)
var rvBuildOut = rx(`(?i)(^|/)(node_modules|__pycache__|\.gradle|dist|build|obj|target)/|\.(exe|dll|so|dylib|apk|aab|class|pyc|o|obj|lib|msi)$`)

func rvAddedFiles(c *rvCtx, r *rvResult) {
	var added []string
	if c.diff != nil {
		for _, k := range c.diff.Order {
			if fd := c.diff.Files[k]; fd.New {
				added = append(added, fd.Path)
			}
		}
	} else if out, err := rvGit(c.root, "ls-files", "-z", "--", "."); err == nil {
		for _, p := range strings.Split(out, "\x00") {
			if p = strings.TrimSpace(p); p != "" {
				added = append(added, filepath.ToSlash(p))
			}
		}
	}
	sort.Strings(added)
	big := 0
	for _, p := range added {
		low := strings.ToLower(p)
		switch {
		case rvSecretName.MatchString(p) && !rvEnvSample.MatchString(p):
			r.add(rvIssue{Tool: "slop", File: p, Sev: rvErr, Code: "secret-file", scope: rvScopeFile,
				Msg: "a secrets/key file is in the repo: remove it, add it to .gitignore, and replace the keys it holds"})
		case rvBuildOut.MatchString(low) && !strings.HasSuffix(low, "gradle-wrapper.jar"):
			r.add(rvIssue{Tool: "slop", File: p, Sev: rvWarn, Code: "build-output", scope: rvScopeFile,
				Msg: "build output or a binary is going into git: add it to .gitignore"})
		default:
			if st, err := os.Stat(c.abs(p)); err == nil && st.Size() > 5<<20 && big < 10 {
				big++
				r.add(rvIssue{Tool: "slop", File: p, Sev: rvWarn, Code: "big-file", scope: rvScopeFile,
					Msg: fmt.Sprintf("a %d MB file is going into git: keep big binaries out of the repo (or use Git LFS)", st.Size()>>20)})
			}
		}
	}
}
