package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// deps layer: dependencies with known vulnerabilities (OSV-Scanner) and package names that
// don't exist or are brand new. AI models invent package names, and attackers register those
// names with malware in them ("slopsquatting"), so a new dependency must be a real, known one.

type rvDep struct {
	eco, name, file string
	line            int
	private         bool // the project also uses another registry, so "not found" is only a warning
}

func rvDepsLayer(c *rvCtx) rvResult {
	var r rvResult
	if !c.inv.deps || c.single != "" {
		return r
	}
	if c.diff != nil {
		touched := false
		for _, k := range c.diff.Order {
			if rvIsDepFile(c.diff.Files[k].Path) {
				touched = true
			}
		}
		if !touched {
			return r
		}
	}
	for _, x := range []rvResult{rvOSV(c), rvRegistry(c)} {
		r.ran, r.fails, r.notes = append(r.ran, x.ran...), append(r.fails, x.fails...), append(r.notes, x.notes...)
		r.issues = append(r.issues, x.issues...)
	}
	return r
}

// rvFindLine returns the first line of a file mentioning name as a whole token (or 0).
func rvFindLine(p, name string) int {
	lines, ok := rvReadLines(p)
	if !ok {
		return 0
	}
	re, err := regexp.Compile(`(^|[^\w.-])` + regexp.QuoteMeta(name) + `($|[^\w.-])`)
	if err != nil {
		return 0
	}
	for i, l := range lines {
		if re.MatchString(l) {
			return i + 1
		}
	}
	return 0
}

func rvOSV(c *rvCtx) rvResult {
	var r rvResult
	bin, msg, err := rvTool("osv-scanner")
	if msg != "" {
		r.notes = append(r.notes, msg)
	}
	if err != nil {
		r.fails = append(r.fails, "osv-scanner: "+err.Error())
		return r
	}
	out := rvTmp("osv", ".json")
	defer os.Remove(out)
	_, se, code, err := ckRun(c.root, 10*time.Minute, bin, "scan", "source", "-r", c.root, "--format", "json", "--output-file", out, "--allow-no-lockfiles")
	if err != nil {
		r.fails = append(r.fails, "osv-scanner: "+err.Error())
		return r
	}
	b, rerr := os.ReadFile(out)
	if rerr != nil || len(b) == 0 || (code > 1 && code != 128) {
		r.fails = append(r.fails, fmt.Sprintf("osv-scanner (exit %d, needs internet): %s", code, ckFirstLines(se, 2)))
		return r
	}
	var res struct {
		Results []struct {
			Source struct {
				Path string `json:"path"`
			} `json:"source"`
			Packages []struct {
				Package struct {
					Name      string `json:"name"`
					Version   string `json:"version"`
					Ecosystem string `json:"ecosystem"`
				} `json:"package"`
				Vulnerabilities []struct {
					ID       string   `json:"id"`
					Summary  string   `json:"summary"`
					Aliases  []string `json:"aliases"`
					Affected []struct {
						Package struct {
							Name string `json:"name"`
						} `json:"package"`
						Ranges []struct {
							Type   string              `json:"type"`
							Events []map[string]string `json:"events"`
						} `json:"ranges"`
					} `json:"affected"`
				} `json:"vulnerabilities"`
				Groups []struct {
					IDs         []string `json:"ids"`
					Aliases     []string `json:"aliases"`
					MaxSeverity string   `json:"max_severity"`
				} `json:"groups"`
			} `json:"packages"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &res); err != nil {
		r.fails = append(r.fails, "osv-scanner: unreadable output")
		return r
	}
	r.ran = append(r.ran, "osv-scanner")
	for _, src := range res.Results {
		rel := rvRel(c.root, src.Source.Path)
		for _, p := range src.Packages {
			var ids []string
			worst, fix, summary := 0.0, "", ""
			fixOK := true
			for _, v := range p.Vulnerabilities {
				id := v.ID
				for _, a := range v.Aliases {
					if strings.HasPrefix(a, "CVE-") {
						id += " (" + a + ")"
						break
					}
				}
				ids = append(ids, id)
				if summary == "" {
					summary = v.Summary
				}
				vf := ""
				for _, af := range v.Affected {
					if af.Package.Name != "" && !strings.EqualFold(af.Package.Name, p.Package.Name) {
						continue
					}
					for _, rg := range af.Ranges {
						if rg.Type == "GIT" {
							continue
						}
						for _, ev := range rg.Events {
							if f := ev["fixed"]; f != "" && (vf == "" || rvVerLess(vf, f)) {
								vf = f
							}
						}
					}
				}
				if vf == "" {
					fixOK = false
				} else if fix == "" || rvVerLess(fix, vf) {
					fix = vf
				}
			}
			if len(p.Groups) > 0 { // groups merge the same issue reported under several IDs
				ids = ids[:0]
				for _, g := range p.Groups {
					if sc, err := strconv.ParseFloat(g.MaxSeverity, 64); err == nil && sc > worst {
						worst = sc
					}
					if len(g.IDs) == 0 {
						continue
					}
					id := g.IDs[0]
					for _, a := range append(append([]string{}, g.IDs...), g.Aliases...) {
						if strings.HasPrefix(a, "CVE-") && a != id {
							id += " (" + a + ")"
							break
						}
					}
					ids = append(ids, id)
				}
			}
			if len(ids) == 0 {
				continue
			}
			sev := rvWarn
			if worst >= 7 {
				sev = rvErr
			}
			m := fmt.Sprintf("%s %s has %d known vulnerabilit", p.Package.Name, p.Package.Version, len(ids))
			if len(ids) == 1 {
				m += "y " + ids[0]
			} else {
				m += "ies: " + strings.Join(ids[:min(3, len(ids))], ", ")
				if len(ids) > 3 {
					m += ", …"
				}
			}
			if summary != "" && len(ids) == 1 {
				m += ": " + rvFirstSentence(summary, 100)
			}
			switch {
			case fix != "" && fixOK:
				m += " — upgrade to " + fix + " or later"
			case fix != "":
				m += " — upgrading to " + fix + " fixes some; check the rest"
			default:
				m += " — no fixed version yet; consider another package"
			}
			if worst > 0 {
				m += fmt.Sprintf(" [worst CVSS %.1f]", worst)
			}
			r.add(rvIssue{Tool: "osv", File: rel, Line: rvFindLine(src.Source.Path, p.Package.Name), Sev: sev, Code: "vulnerable", Msg: m})
		}
	}
	return r
}

// rvVerLess compares dotted versions number by number (1.2.10 > 1.2.9); other text compares as text.
func rvVerLess(a, b string) bool {
	pa, pb := strings.FieldsFunc(a, func(r rune) bool { return r == '.' || r == '-' || r == '+' }), strings.FieldsFunc(b, func(r rune) bool { return r == '.' || r == '-' || r == '+' })
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, ex := strconv.Atoi(pa[i])
		y, ey := strconv.Atoi(pb[i])
		switch {
		case ex == nil && ey == nil && x != y:
			return x < y
		case (ex != nil || ey != nil) && pa[i] != pb[i]:
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// ---- manifests ----

var (
	rvReqName     = regexp.MustCompile(`^\s*([A-Za-z0-9][A-Za-z0-9._-]*)`)
	rvQuoted      = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)
	rvTomlHeader  = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(#.*)?$`)
	rvTomlKey     = regexp.MustCompile(`^\s*"?([A-Za-z0-9][A-Za-z0-9._-]*)"?\s*=`)
	rvGradleDep   = regexp.MustCompile(`\b(?:implementation|api|compileOnly|runtimeOnly|testImplementation|androidTestImplementation|debugImplementation|releaseImplementation|kapt|ksp|annotationProcessor|classpath|lintChecks|coreLibraryDesugaring|testRuntimeOnly|testCompileOnly)\s*\(?\s*(?:(?:platform|enforcedPlatform)\s*\(\s*)?["']([A-Za-z0-9_.\-]+):([A-Za-z0-9_.\-]+)(?::[^"'@]*)?["']`)
	rvCatalogMod  = regexp.MustCompile(`module\s*=\s*["']([A-Za-z0-9_.\-]+):([A-Za-z0-9_.\-]+)["']`)
	rvCatalogGrp  = regexp.MustCompile(`group\s*=\s*["']([A-Za-z0-9_.\-]+)["'].*?\bname\s*=\s*["']([A-Za-z0-9_.\-]+)["']`)
	rvCatalogStr  = regexp.MustCompile(`=\s*["']([A-Za-z0-9_.\-]+):([A-Za-z0-9_.\-]+):[^"']+["']\s*$`)
	rvCustomMaven = regexp.MustCompile(`\bmaven\s*[({]`)
)

func rvPyNorm(n string) string {
	return strings.ToLower(regexp.MustCompile(`[-_.]+`).ReplaceAllString(n, "-"))
}

func rvPySpec(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "-") || strings.Contains(s, "://") || strings.HasPrefix(s, "git+") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "/") || strings.Contains(s, " @ ") {
		return ""
	}
	if m := rvReqName.FindStringSubmatch(s); m != nil {
		return rvPyNorm(m[1])
	}
	return ""
}

func rvParseRequirements(rel string, lines []string) []rvDep {
	private := false
	var out []rvDep
	for i, l := range lines {
		t := strings.TrimSpace(strings.SplitN(l, " #", 2)[0])
		if strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "-i") || strings.HasPrefix(t, "--index-url") || strings.HasPrefix(t, "--extra-index-url") {
			private = true
			continue
		}
		if n := rvPySpec(t); n != "" {
			out = append(out, rvDep{eco: "py", name: n, file: rel, line: i + 1})
		}
	}
	for i := range out {
		out[i].private = private
	}
	return out
}

// rvParsePyproject reads PEP 621, PEP 735 and Poetry dependency lists.
func rvParsePyproject(rel string, lines []string) []rvDep {
	var out []rvDep
	table, inArray, private := "", false, false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") || t == "" {
			continue
		}
		if m := rvTomlHeader.FindStringSubmatch(t); m != nil && !inArray {
			table = m[1]
			if table == "tool.poetry.source" || table == "tool.uv.index" || strings.HasPrefix(table, "tool.pdm.source") {
				private = true
			}
			continue
		}
		poetry := table == "tool.poetry.dependencies" || table == "tool.poetry.dev-dependencies" || (strings.HasPrefix(table, "tool.poetry.group.") && strings.HasSuffix(table, ".dependencies"))
		listy := table == "project" || table == "project.optional-dependencies" || table == "dependency-groups"
		switch {
		case poetry:
			if m := rvTomlKey.FindStringSubmatch(t); m != nil && !strings.EqualFold(m[1], "python") && !strings.Contains(t, "path =") && !strings.Contains(t, "git =") {
				out = append(out, rvDep{eco: "py", name: rvPyNorm(m[1]), file: rel, line: i + 1})
			}
		case listy:
			if !inArray {
				eq := strings.Index(t, "=")
				if eq < 0 || !strings.HasPrefix(strings.TrimSpace(t[eq+1:]), "[") {
					continue
				}
				key := strings.TrimSpace(t[:eq])
				if table == "project" && key != "dependencies" {
					continue
				}
				inArray = true
				t = t[eq+1:]
			}
			for _, m := range rvQuoted.FindAllStringSubmatch(t, -1) {
				s := m[1] + m[2]
				if n := rvPySpec(s); n != "" {
					out = append(out, rvDep{eco: "py", name: n, file: rel, line: i + 1})
				}
			}
			if strings.Contains(t, "]") {
				inArray = false
			}
		}
	}
	for i := range out {
		out[i].private = private
	}
	return out
}

func rvParsePackageJSON(rel string, text string, private bool) []rvDep {
	var pj map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &pj) != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	lineOf := func(name string) int {
		k := `"` + name + `"`
		for i, l := range lines {
			if strings.Contains(l, k) && strings.Contains(l[strings.Index(l, k)+len(k):], ":") {
				return i + 1
			}
		}
		return 0
	}
	var out []rvDep
	for _, sec := range []string{"dependencies", "devDependencies", "peerDependencies", "optionalDependencies"} {
		var deps map[string]string
		if json.Unmarshal(pj[sec], &deps) != nil {
			continue
		}
		for name, spec := range deps {
			key := name
			s := strings.ToLower(strings.TrimSpace(spec))
			if strings.HasPrefix(s, "npm:") {
				target := strings.TrimPrefix(spec, "npm:")
				if i := strings.LastIndex(target, "@"); i > 0 {
					target = target[:i]
				}
				name, s = target, "1"
			}
			if strings.HasPrefix(s, "file:") || strings.HasPrefix(s, "link:") || strings.HasPrefix(s, "workspace:") || strings.HasPrefix(s, "portal:") ||
				strings.HasPrefix(s, "patch:") || strings.Contains(s, "://") || strings.HasPrefix(s, "git") || strings.HasPrefix(s, "github:") || strings.Contains(s, "/") && !strings.HasPrefix(s, "^") && !strings.HasPrefix(s, "~") && !regexp.MustCompile(`^[\d<>=~^*x. |-]+$`).MatchString(s) {
				continue
			}
			out = append(out, rvDep{eco: "npm", name: name, file: rel, line: lineOf(key), private: private})
		}
	}
	return out
}

func rvParseGradle(rel string, lines []string) []rvDep {
	var out []rvDep
	for i, l := range lines {
		code, _ := rvSplitComment("kt", l)
		for _, m := range rvGradleDep.FindAllStringSubmatch(code, -1) {
			out = append(out, rvDep{eco: "maven", name: m[1] + ":" + m[2], file: rel, line: i + 1})
		}
	}
	return out
}

func rvParseCatalog(rel string, lines []string) []rvDep {
	var out []rvDep
	table := ""
	for i, l := range lines {
		t := strings.TrimSpace(strings.SplitN(l, "#", 2)[0])
		if m := rvTomlHeader.FindStringSubmatch(t); m != nil {
			table = m[1]
			continue
		}
		if table != "libraries" {
			continue
		}
		for _, re := range []*regexp.Regexp{rvCatalogMod, rvCatalogGrp, rvCatalogStr} {
			if m := re.FindStringSubmatch(t); m != nil {
				out = append(out, rvDep{eco: "maven", name: m[1] + ":" + m[2], file: rel, line: i + 1})
				break
			}
		}
	}
	return out
}

func rvParseCargo(rel string, lines []string, private bool) []rvDep {
	var out []rvDep
	table := ""
	for i, l := range lines {
		t := strings.TrimSpace(strings.SplitN(l, "#", 2)[0])
		if m := rvTomlHeader.FindStringSubmatch(t); m != nil {
			table = m[1]
			continue
		}
		if !(table == "dependencies" || table == "dev-dependencies" || table == "build-dependencies" || (strings.HasPrefix(table, "target.") && strings.HasSuffix(table, "dependencies"))) {
			continue
		}
		m := rvTomlKey.FindStringSubmatch(t)
		if m == nil || strings.Contains(t, "path =") || strings.Contains(t, "git =") || strings.Contains(t, "workspace = true") {
			continue
		}
		name := m[1]
		if pm := regexp.MustCompile(`package\s*=\s*"([^"]+)"`).FindStringSubmatch(t); pm != nil {
			name = pm[1]
		}
		out = append(out, rvDep{eco: "cargo", name: name, file: rel, line: i + 1, private: private})
	}
	return out
}

func rvManifestDeps(c *rvCtx) []rvDep {
	npmPrivate := ckFileHas(filepath.Join(c.root, ".npmrc"), "registry")
	cargoPrivate := ckFileHas(filepath.Join(c.root, ".cargo", "config.toml"), "[registries")
	mavenPrivate := false
	var deps []rvDep
	for _, f := range c.inv.files {
		b := strings.ToLower(filepath.Base(f))
		if b == "build.gradle" || b == "build.gradle.kts" || b == "settings.gradle" || b == "settings.gradle.kts" {
			if lines, ok := rvReadLines(c.abs(f)); ok {
				for _, l := range lines {
					if code, _ := rvSplitComment("kt", l); rvCustomMaven.MatchString(code) && !strings.Contains(code, "jitpack") {
						mavenPrivate = true
					}
				}
			}
		}
	}
	for _, f := range c.inv.files {
		if rvPathSkipped(f, c.ign) {
			continue
		}
		b := strings.ToLower(filepath.Base(f))
		lines, ok := rvReadLines(c.abs(f))
		if !ok {
			continue
		}
		switch {
		case strings.HasSuffix(b, ".txt") && rvIsDepFile(f):
			deps = append(deps, rvParseRequirements(f, lines)...)
		case b == "pyproject.toml":
			deps = append(deps, rvParsePyproject(f, lines)...)
		case b == "package.json" && !strings.Contains("/"+f, "/node_modules/"):
			deps = append(deps, rvParsePackageJSON(f, strings.Join(lines, "\n"), npmPrivate)...)
		case b == "build.gradle" || b == "build.gradle.kts":
			for _, d := range rvParseGradle(f, lines) {
				d.private = mavenPrivate
				deps = append(deps, d)
			}
		case b == "libs.versions.toml":
			for _, d := range rvParseCatalog(f, lines) {
				d.private = mavenPrivate
				deps = append(deps, d)
			}
		case b == "cargo.toml":
			deps = append(deps, rvParseCargo(f, lines, cargoPrivate)...)
		}
	}
	return deps
}

// ---- registry lookups (deps.dev first, then the registry itself) ----

type rvRegState struct {
	State string    `json:"state"` // ok, missing, new, unindexed
	Age   int       `json:"age_days"`
	At    time.Time `json:"at"`
}

var rvRegMu sync.Mutex

func rvRegCachePath() string { return filepath.Join(checkerDir(), "cache", "registry.json") }

var rvHTTP = &http.Client{Timeout: 20 * time.Second}

func rvGet(u string, accept string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), "GET", u, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", "codereview/1 (dependency check)")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := rvHTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var b []byte
	if resp.StatusCode == 200 {
		b, _ = io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	}
	return resp.StatusCode, b, nil
}

var rvDepsSystem = map[string]string{"py": "PYPI", "npm": "NPM", "maven": "MAVEN", "cargo": "CARGO"}

func rvRegistryName(eco string) string {
	return map[string]string{"py": "PyPI", "npm": "npm", "maven": "Maven Central or Google's Maven", "cargo": "crates.io"}[eco]
}

func rvNativeExists(eco, name string) (bool, error) {
	var urls []string
	switch eco {
	case "py":
		urls = []string{"https://pypi.org/pypi/" + url.PathEscape(name) + "/json"}
	case "npm":
		urls = []string{"https://registry.npmjs.org/" + strings.Replace(url.PathEscape(name), "%40", "@", 1)}
	case "maven":
		g, a, _ := strings.Cut(name, ":")
		p := strings.ReplaceAll(g, ".", "/") + "/" + a + "/maven-metadata.xml"
		urls = []string{"https://repo1.maven.org/maven2/" + p, "https://dl.google.com/android/maven2/" + p}
	case "cargo":
		urls = []string{"https://crates.io/api/v1/crates/" + url.PathEscape(name)}
	}
	var lastErr error
	for _, u := range urls {
		code, _, err := rvGet(u, "application/vnd.npm.install-v1+json")
		if err != nil {
			lastErr = err
			continue
		}
		if code == 200 {
			return true, nil
		}
		if code != 404 && code != 410 {
			lastErr = fmt.Errorf("HTTP %d", code)
		}
	}
	if lastErr != nil {
		return false, lastErr
	}
	return false, nil
}

func rvLookup(eco, name string) (rvRegState, error) {
	sys := rvDepsSystem[eco]
	code, body, err := rvGet("https://api.deps.dev/v3/systems/"+sys+"/packages/"+url.PathEscape(name), "")
	if err != nil {
		return rvRegState{}, err
	}
	now := time.Now()
	if code == 200 {
		var res struct {
			Versions []struct {
				PublishedAt string `json:"publishedAt"`
			} `json:"versions"`
		}
		json.Unmarshal(body, &res)
		var first time.Time
		for _, v := range res.Versions {
			if t, err := time.Parse(time.RFC3339, v.PublishedAt); err == nil && (first.IsZero() || t.Before(first)) {
				first = t
			}
		}
		st := rvRegState{State: "ok", At: now}
		if !first.IsZero() {
			st.Age = int(now.Sub(first).Hours() / 24)
			if st.Age < 30 {
				st.State = "new"
			}
		}
		return st, nil
	}
	if code != 404 {
		return rvRegState{}, fmt.Errorf("deps.dev HTTP %d", code)
	}
	ok, err := rvNativeExists(eco, name)
	if err != nil {
		return rvRegState{}, err
	}
	switch {
	case !ok:
		return rvRegState{State: "missing", At: now}, nil
	case eco == "maven":
		return rvRegState{State: "ok", At: now}, nil
	}
	return rvRegState{State: "unindexed", At: now}, nil
}

func rvRegistry(c *rvCtx) rvResult {
	var r rvResult
	deps := rvManifestDeps(c)
	var todo []rvDep
	seen := map[string]bool{}
	for _, d := range deps {
		if c.diff != nil {
			fd := c.diff.Files[rvKey(d.file)]
			if fd == nil || (!fd.New && !fd.Lines[d.line]) {
				continue
			}
		}
		k := d.eco + ":" + d.name
		if seen[k] {
			continue
		}
		seen[k] = true
		todo = append(todo, d)
	}
	if len(todo) == 0 {
		return r
	}
	limit := 60
	if c.diff == nil {
		limit = 150
	}
	if len(todo) > limit {
		r.notes = append(r.notes, fmt.Sprintf("package names: checked the first %d of %d", limit, len(todo)))
		todo = todo[:limit]
	}
	cache := map[string]rvRegState{}
	rvRegMu.Lock()
	if b, err := os.ReadFile(rvRegCachePath()); err == nil {
		json.Unmarshal(b, &cache)
	}
	rvRegMu.Unlock()
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var netErr error
	states := make([]rvRegState, len(todo))
	for i, d := range todo {
		k := d.eco + ":" + d.name
		mu.Lock()
		st, ok := cache[k]
		mu.Unlock()
		ttl := 7 * 24 * time.Hour
		if st.State == "missing" || st.State == "unindexed" || st.State == "new" {
			ttl = 6 * time.Hour
		}
		if ok && time.Since(st.At) < ttl {
			states[i] = st
			continue
		}
		wg.Add(1)
		go func(i int, d rvDep, k string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := rvLookup(d.eco, d.name)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				netErr = err
				return
			}
			states[i] = st
			cache[k] = st
		}(i, d, k)
	}
	wg.Wait()
	rvRegMu.Lock()
	os.MkdirAll(filepath.Dir(rvRegCachePath()), 0o755)
	if b, err := json.Marshal(cache); err == nil {
		os.WriteFile(rvRegCachePath(), b, 0o644)
	}
	rvRegMu.Unlock()
	checked := 0
	for i, d := range todo {
		st := states[i]
		if st.State == "" {
			continue
		}
		checked++
		reg := rvRegistryName(d.eco)
		switch st.State {
		case "missing":
			sev, msg := rvErr, fmt.Sprintf("%q is not on %s: a made-up package name? AI models invent names like this, and attackers register them with malware. Use the real package", d.name, reg)
			if d.private {
				sev, msg = rvWarn, fmt.Sprintf("%q is not on public %s: fine only if it comes from your own registry", d.name, reg)
			}
			r.add(rvIssue{Tool: "deps", File: d.file, Line: d.line, Sev: sev, Code: "made-up-package", Msg: msg})
		case "new":
			r.add(rvIssue{Tool: "deps", File: d.file, Line: d.line, Sev: rvWarn, Code: "new-package",
				Msg: fmt.Sprintf("%q was first published %d day(s) ago: make sure it's the package you meant (new look-alike names are a known attack)", d.name, st.Age)})
		case "unindexed":
			r.add(rvIssue{Tool: "deps", File: d.file, Line: d.line, Sev: rvWarn, Code: "new-package",
				Msg: fmt.Sprintf("%q exists on %s but is too new or obscure to be indexed anywhere else: check it's the real package", d.name, reg)})
		}
	}
	if checked > 0 {
		r.ran = append(r.ran, "package names")
	}
	if netErr != nil {
		r.fails = append(r.fails, "package-name check (needs internet): "+netErr.Error())
	}
	return r
}
