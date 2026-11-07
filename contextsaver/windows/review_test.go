package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRvParseDiff(t *testing.T) {
	d := rvParseDiff(`diff --git a/app.py b/app.py
index 1..2 100644
--- a/app.py
+++ b/app.py
@@ -3,0 +4,2 @@ def f():
+    x = 1
+    return x
@@ -10 +12 @@
--- removed sql comment line
+-- new sql comment
diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package main
+func main() {}
diff --git a/old_test.go b/old_test.go
deleted file mode 100644
--- a/old_test.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package main
-func TestX(t *testing.T) {}
diff --git "a/sp ace.txt" "b/sp ace.txt"
--- "a/sp ace.txt"
+++ "b/sp ace.txt"
@@ -1 +1 @@
-a
+b
`)
	py := d.Files[rvKey("app.py")]
	if py == nil || !py.Lines[4] || !py.Lines[5] || !py.Lines[12] || py.Lines[6] || len(py.Removed) != 1 || py.Removed[0] != "-- removed sql comment line" {
		t.Fatalf("app.py diff: %+v", py)
	}
	if g := d.Files[rvKey("new.go")]; g == nil || !g.New || len(g.Added) != 2 {
		t.Fatalf("new.go: %+v", g)
	}
	if len(d.Deleted) != 1 || d.Deleted[0] != "old_test.go" || d.Files[rvKey("old_test.go")] != nil {
		t.Fatalf("deleted: %v", d.Deleted)
	}
	if d.Files[rvKey("sp ace.txt")] == nil {
		t.Fatalf("quoted path missing: %v", d.Order)
	}
	is := rvIssue{File: "app.py", Line: 5}
	if !d.isNew(&is) {
		t.Fatal("line 5 should be new")
	}
	is = rvIssue{File: "app.py", Line: 7}
	if d.isNew(&is) {
		t.Fatal("line 7 is old")
	}
	is = rvIssue{File: "app.py", Line: 1, end: 4}
	if !d.isNew(&is) {
		t.Fatal("range 1-4 touches line 4")
	}
	is = rvIssue{File: "other.py", Line: 1, scope: rvScopeFile}
	if d.isNew(&is) {
		t.Fatal("other.py didn't change")
	}
}

// slopOf runs the slop checks on one new file and returns the codes found.
func slopOf(t *testing.T, name, src string) map[string]int {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, filepath.FromSlash(name))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &rvCtx{root: dir, inv: &rvInv{byLang: map[string][]string{}}}
	var r rvResult
	rvSlopFile(c, &r, name, nil)
	got := map[string]int{}
	for _, is := range r.issues {
		got[is.Code]++
	}
	return got
}

func TestRvSlopComments(t *testing.T) {
	got := slopOf(t, "app.go", `package main

func a() {
	// ... rest of the code remains the same
}

func b() {
	// TODO: implement this properly
}

// In a real application you would validate the token here.
func c() { panic("not implemented") }

func d(err error) {
	_ = err
}
var key = "your-api-key-here"
var p = "C:\\Users\\bob\\data.txt"
`)
	for _, want := range []string{"elided-code", "unfinished", "demo-shortcut", "stub", "swallowed-error", "placeholder-value", "hardcoded-path"} {
		if got[want] == 0 {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	clean := slopOf(t, "ok.go", `package main

// Keep the existing behaviour when the flag is off.
// The rest of this file handles retries.
func ok() error { return nil }
var s = "http://example.com/a//b" // not a comment start
`)
	if len(clean) != 0 {
		t.Errorf("false positives: %v", clean)
	}
}

func TestRvSlopStubsAndSuppressions(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"a.ts", "export function f(): number {\n  throw new Error(\"Not implemented\");\n}\n", "stub"},
		{"A.kt", "fun f(): Int = TODO(\"later\")\n", "stub"},
		{"a.rs", "fn f() -> i32 { todo!() }\n", "stub"},
		{"A.cs", "int F() { throw new NotImplementedException(); }\n", "stub"},
		{"a.py", "x = compute()  # type: ignore\n", "suppression"},
		{"a.js", "// eslint-disable-next-line no-eval\neval(x)\n", "suppression"},
		{"a.go", "package main\nfunc f() { g() //nolint:errcheck\n}\n", "suppression"},
		{"app.py", "import pdb\n", "debug-leftover"},
		{"a.txt", "<<<<<<< HEAD\nx\n=======\ny\n>>>>>>> branch\n", "conflict-marker"},
		{"b.py", "Here's the updated code:\ndef f():\n    return 1\n", "llm-text"},
		{"B.java", "class B { void f() { try { g(); } catch (Exception e) {} } }\n", "swallowed-error"},
		{"C.java", "class C {\n void f() {\n  g();\n", "unbalanced-braces"},
	}
	for _, tc := range cases {
		if got := slopOf(t, tc.name, tc.src); got[tc.want] == 0 {
			t.Errorf("%s: want %s, got %v", tc.name, tc.want, got)
		}
	}
	if got := slopOf(t, "ok.kt", "fun f() = listOf(1).map { it * 2 }\nval todoList = mutableListOf<String>()\n"); got["stub"] != 0 {
		t.Errorf("Kotlin false stub: %v", got)
	}
}

func TestRvPyStubs(t *testing.T) {
	got := slopOf(t, "svc.py", `from abc import ABC, abstractmethod

class Base(ABC):
    @abstractmethod
    def run(self):
        ...

class Impl:
    def __init__(self):
        pass

    def run(self):
        """Run it."""
        raise NotImplementedError

    def stop(self):
        pass

    def ok(self):
        return 1
`)
	if got["stub"] != 2 {
		t.Fatalf("want 2 stubs (run raises, stop passes), got %v", got)
	}
}

func TestRvTests(t *testing.T) {
	got := slopOf(t, "calc_test.go", `package calc

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("bad")
	}
}

func TestHollow(t *testing.T) {
	Add(1, 2)
}

func TestHelper(t *testing.T) {
	checkAdd(t, 1, 2)
}

func TestSkipped(t *testing.T) {
	t.Skip("flaky")
	if Add(1, 1) != 2 {
		t.Error("x")
	}
}
`)
	if got["hollow-test"] != 1 || got["test-skipped"] != 1 {
		t.Fatalf("go tests: %v", got)
	}
	got = slopOf(t, "tests/test_calc.py", `def test_ok():
    assert add(1, 2) == 3

def test_hollow():
    add(1, 2)

def test_trivial():
    assert True
`)
	if got["hollow-test"] != 1 || got["trivial-assert"] != 1 {
		t.Fatalf("py tests: %v", got)
	}
	got = slopOf(t, "calc.test.ts", `describe("calc", () => {
  it.only("adds", () => {
    expect(add(1, 2)).toBe(3);
  });
  it("does nothing", () => {
    add(1, 2);
  });
  test("always", () => {
    expect(true).toBe(true);
  });
});
`)
	if got["hollow-test"] != 1 || got["test-focused"] != 1 || got["trivial-assert"] != 1 {
		t.Fatalf("ts tests: %v", got)
	}
	got = slopOf(t, "app/src/test/CalcTest.kt", `class CalcTest {
    @Test
    fun adds() {
        assertEquals(3, add(1, 2))
    }

    @Test
    fun hollow() {
        add(1, 2)
    }

    @Ignore
    @Test
    fun later() {
        assertTrue(true)
    }
}
`)
	if got["hollow-test"] != 1 || got["test-skipped"] != 1 || got["trivial-assert"] != 1 {
		t.Fatalf("kotlin tests: %v", got)
	}
}

func TestRvTestIntegrity(t *testing.T) {
	d := rvParseDiff(`diff --git a/calc_test.go b/calc_test.go
--- a/calc_test.go
+++ b/calc_test.go
@@ -5,8 +5,1 @@
-func TestA(t *testing.T) {
-	if A() != 1 {
-		t.Fatal("a")
-	}
-	if B() != 2 {
-		t.Errorf("b")
-	}
-}
+// tests moved
diff --git a/calc.go b/calc.go
--- a/calc.go
+++ b/calc.go
@@ -1,0 +2,16 @@
+func A() int { return 1 }
+func B() int { return 2 }
+func C() int { return 3 }
+func D() int { return 4 }
+func E() int { return 5 }
+func F() int { return 6 }
+func G() int { return 7 }
+func H() int { return 8 }
+func I() int { return 9 }
+func J() int { return 10 }
+func K() int { return 11 }
+func L() int { return 12 }
+func M() int { return 13 }
+func N() int { return 14 }
+func O() int { return 15 }
+func P() int { return 16 }
`)
	c := &rvCtx{root: t.TempDir(), diff: d}
	var r rvResult
	rvTestIntegrity(c, &r)
	got := map[string]int{}
	for _, is := range r.issues {
		got[is.Code]++
	}
	if got["tests-removed"] != 1 || got["asserts-removed"] != 1 || got["no-tests"] != 0 {
		t.Fatalf("integrity: %v", got)
	}
}

func TestRvDupBlocks(t *testing.T) {
	dir := t.TempDir()
	block := `	total := 0
	for _, x := range items {
		if x.Price > 0 {
			total += x.Price * x.Qty
		}
	}
	if total > limit {
		total = limit
	}
	log.Printf("total %d", total)
	return total
`
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc a(items []item, limit int) int {\n"+block+"}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.go"), []byte("package main\n\nfunc b(items []item, limit int) int {\n"+block+"}\n"), 0o644)
	c := &rvCtx{root: dir, inv: rvInventory(dir, "", nil)}
	var r rvResult
	rvDupBlocks(c, &r)
	if len(r.issues) != 1 || r.issues[0].Code != "copied-block" {
		t.Fatalf("dups: %+v", r.issues)
	}
}

func TestRvManifests(t *testing.T) {
	req := rvParseRequirements("requirements.txt", strings.Split("requests==2.31.0\n# comment\n-e .\nFlask_Login>=0.6 ; python_version>'3.8'\ngit+https://github.com/x/y\nfastjsonx-utils\n", "\n"))
	if len(req) != 3 || req[1].name != "flask-login" || req[2].name != "fastjsonx-utils" || req[2].line != 6 {
		t.Fatalf("requirements: %+v", req)
	}
	pp := rvParsePyproject("pyproject.toml", strings.Split(`[project]
name = "x"
dependencies = [
  "httpx>=0.27",
  "pydantic[email]",
]
[project.optional-dependencies]
dev = ["pytest"]
[tool.poetry.dependencies]
python = "^3.11"
rich = "^13"
`, "\n"))
	names := []string{}
	for _, d := range pp {
		names = append(names, d.name)
	}
	if strings.Join(names, ",") != "httpx,pydantic,pytest,rich" {
		t.Fatalf("pyproject: %v", names)
	}
	pj := rvParsePackageJSON("package.json", `{
  "dependencies": {"express": "^4.19.2", "local": "file:../x", "alias": "npm:real-pkg@^1"},
  "devDependencies": {"@types/node": "^22"}
}`, false)
	got := map[string]int{}
	for _, d := range pj {
		got[d.name] = d.line
	}
	if len(pj) != 3 || got["express"] != 2 || got["@types/node"] != 3 || got["real-pkg"] == 0 {
		t.Fatalf("package.json: %+v", pj)
	}
	gr := rvParseGradle("app/build.gradle.kts", strings.Split(`dependencies {
    implementation("androidx.core:core-ktx:1.13.1")
    implementation(platform("androidx.compose:compose-bom:2024.09.00"))
    testImplementation 'junit:junit:4.13.2'
    implementation(libs.foo)
}`, "\n"))
	if len(gr) != 3 || gr[1].name != "androidx.compose:compose-bom" {
		t.Fatalf("gradle: %+v", gr)
	}
	cat := rvParseCatalog("gradle/libs.versions.toml", strings.Split(`[versions]
x = "1"
[libraries]
a = { module = "com.squareup.okhttp3:okhttp", version.ref = "x" }
b = { group = "io.coil-kt", name = "coil", version = "2" }
c = "org.jetbrains:annotations:24.0"
[plugins]
p = { id = "x", version = "1" }
`, "\n"))
	if len(cat) != 3 || cat[1].name != "io.coil-kt:coil" {
		t.Fatalf("catalog: %+v", cat)
	}
	cg := rvParseCargo("Cargo.toml", strings.Split(`[dependencies]
serde = { version = "1", features = ["derive"] }
local = { path = "../local" }
tokio = "1"
[dev-dependencies]
pretty = { package = "pretty_assertions", version = "1" }
`, "\n"), false)
	if len(cg) != 3 || cg[2].name != "pretty_assertions" {
		t.Fatalf("cargo: %+v", cg)
	}
}

func TestRvSeverities(t *testing.T) {
	if rvGrepSev("security", []string{"vuln"}, "MEDIUM", "WARNING") != rvErr || rvGrepSev("security", []string{"audit"}, "LOW", "ERROR") != rvWarn ||
		rvGrepSev("best-practice", nil, "", "INFO") != -1 || rvGrepSev("security", []string{"audit"}, "LOW", "INFO") != -1 {
		t.Fatal("opengrep severities")
	}
	if rvGoSev("gosec", "G402", "high") != rvErr || rvGoSev("gosec", "G304", "medium") != rvWarn || rvGoSev("staticcheck", "SA4006", "") != rvErr ||
		rvGoSev("staticcheck", "ST1003", "") != rvNote || rvGoSev("errcheck", "", "") != rvWarn {
		t.Fatal("golangci severities")
	}
	if s, sc := rvRuffSev("F821"); s != rvErr || sc != rvScopeFile {
		t.Fatal("ruff F821")
	}
	if s, _ := rvRuffSev("S602"); s != rvWarn {
		t.Fatal("ruff S602")
	}
}

func TestRvEnvHint(t *testing.T) {
	if h := rvEnvHint("ERROR: JAVA_HOME is set to an invalid directory: C:\\nope\\jdk\n"); !strings.Contains(h, `C:\nope\jdk`) {
		t.Fatalf("java hint: %q", h)
	}
	if rvEnvHint("--- FAIL: TestX") != "" {
		t.Fatal("a failing test is not a machine problem")
	}
}

func TestRvParseAI(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n\nfunc f(xs []int) int {\n\tfor i := 0; i <= len(xs); i++ {\n\t}\n\treturn 0\n}\n"), 0o644)
	c := &rvCtx{root: dir}
	ans := "Here are the problems:\n- a.go:4: high: loop runs one past the end :: for i := 0; i <= len(xs); i++ {\na.go:6: medium: always returns zero :: return 1\nmissing.go:3: high: x :: y\na.go:2: low: nit :: package main\n"
	got, dropped := rvParseAI(c, ans)
	if len(got) != 1 || got[0].Line != 4 || dropped != 2 {
		t.Fatalf("ai claims: %+v dropped %d", got, dropped)
	}
}

func TestRvStringMask(t *testing.T) {
	got := slopOf(t, "fixtures_test.go", "package x\n\nvar src = `\nfunc TestFake(t *testing.T) {\n\t// ... rest of the code\n}\n`\n\nfunc TestReal(t *testing.T) {\n\tif src == \"\" {\n\t\tt.Fatal(\"empty\")\n\t}\n}\n")
	if len(got) != 0 {
		t.Fatalf("text inside a raw string was taken for code: %v", got)
	}
	mask := rvStringMask("py", strings.Split("x = 1\ns = \"\"\"\n# not a comment\n\"\"\"\ny = 2", "\n"))
	if mask[0] || !mask[2] || mask[4] {
		t.Fatalf("python mask: %v", mask)
	}
}

func TestRvSplitComment(t *testing.T) {
	for _, tc := range []struct{ lang, in, code, comment string }{
		{"go", `x := "a // b" // real`, `x := "a // b" `, " real"},
		{"py", `s = "#x"  # note`, `s = "#x"  `, " note"},
		{"js", `  * block line`, "", "block line"},
		{"rs", `let c = 'a'; // c`, `let c = 'a'; `, " c"},
	} {
		code, comment := rvSplitComment(tc.lang, tc.in)
		if code != tc.code || comment != tc.comment {
			t.Errorf("%s %q: got %q / %q", tc.lang, tc.in, code, comment)
		}
	}
}

// TestRvReviewRepo runs a whole review (our own checks only, no downloads) on a git repo
// whose last change is typical unfinished AI work.
func TestRvReviewRepo(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	appHome = t.TempDir()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command(git, append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "core.autocrlf=false", "-c", "commit.gpgsign=false"}, args...)...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write := func(name, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write("calc.py", "def add(a, b):\n    return a + b\n")
	write("tests/test_calc.py", "from calc import add\n\ndef test_add():\n    assert add(1, 2) == 3\n")
	run("add", ".")
	run("commit", "-q", "-m", "first")
	write("calc.py", "def add(a, b):\n    return a + b\n\ndef mul(a, b):\n    # ... rest of the implementation\n    raise NotImplementedError\n")
	write("tests/test_calc.py", "from calc import add, mul\n\ndef test_add():\n    add(1, 2)\n\ndef test_mul():\n    assert True\n")
	write(".env", "API_KEY=abc\n")
	args, _ := json.Marshal(map[string]any{"action": "review", "path": dir, "skip": "lint,security,secrets,deps,ci,scripts,kotlin,complexity,tests"})
	out, err := toolReview(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Verdict: BLOCK", "uncommitted changes", "elided-code", "stub", "hollow-test", "trivial-assert", "secret-file"} {
		if !strings.Contains(out, want) {
			t.Errorf("review output lacks %q:\n%s", want, out)
		}
	}
	var js struct {
		Verdict string
		Issues  []map[string]any
	}
	b, err := os.ReadFile(filepath.Join(checkerDir(), "last-review.json"))
	if err != nil || json.Unmarshal(b, &js) != nil || js.Verdict != "BLOCK" || len(js.Issues) == 0 {
		t.Fatalf("last-review.json: %v %s", err, b)
	}
	run("add", ".")
	run("commit", "-q", "-m", "second")
	out, err = toolReview(args)
	if err != nil || !strings.Contains(out, "last commit") || !strings.Contains(out, "Verdict: BLOCK") {
		t.Fatalf("last-commit review: %v\n%s", err, out)
	}
	args, _ = json.Marshal(map[string]any{"action": "review", "path": dir, "base": "HEAD", "skip": "lint,security,secrets,deps,ci,scripts,kotlin,complexity,tests"})
	if out, err = toolReview(args); err != nil || !strings.Contains(out, "nothing changed") {
		t.Fatalf("no-change review: %v\n%s", err, out)
	}
}
