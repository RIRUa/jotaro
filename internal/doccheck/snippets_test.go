package doccheck

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSnippetsCompile builds every Go block of the Markdown documents (English and Japanese) in a temporary
// module that replaces github.com/RIRUa/jotaro with this checkout. Nothing is run. It needs the go tool and
// the module cache, so it only runs when DOCCHECK_COMPILE is set (scripts/check-docs.sh does).
func TestSnippetsCompile(t *testing.T) {
	if os.Getenv("DOCCHECK_COMPILE") == "" {
		t.Skip("set DOCCHECK_COMPILE=1 (or run scripts/check-docs.sh) to compile the documentation snippets")
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	files, err := markdownFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	var snippets []snippet
	for _, f := range files {
		if skippedDoc(f) {
			continue
		}
		d, err := readDocument(root, f)
		if err != nil {
			t.Fatal(err)
		}
		sn, problems := snippetsOf(d)
		for _, p := range problems {
			t.Error(p)
		}
		snippets = append(snippets, sn...)
	}
	if len(snippets) == 0 {
		t.Fatal(errNoSnippets)
	}

	for _, p := range buildSnippets(t, root, snippets) {
		t.Error(p)
	}
	t.Logf("compiled %d Go snippets", len(snippets))
}

func writeFile(t *testing.T, name, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skippedDoc(p string) bool {
	for _, dir := range snippetsSkipped {
		if strings.HasPrefix(p, dir) {
			return true
		}
	}
	return false
}

// buildSnippets compiles the snippets in a temporary module and returns one message per compile error.
func buildSnippets(t *testing.T, root string, snippets []snippet) []string {
	t.Helper()
	mod := t.TempDir()
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goVersion := "1.25"
	for _, l := range strings.Split(string(goMod), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "go "); ok {
			goVersion = v
		}
	}
	// The replace target is quoted so a path with spaces works; ToSlash keeps a Windows path valid.
	writeFile(t, filepath.Join(mod, "go.mod"), fmt.Sprintf(
		"module doccheck.test\n\ngo %s\n\nrequire github.com/RIRUa/jotaro v0.0.0\n\nreplace github.com/RIRUa/jotaro => %q\n", goVersion, filepath.ToSlash(root)))
	if sum, err := os.ReadFile(filepath.Join(root, "go.sum")); err == nil {
		writeFile(t, filepath.Join(mod, "go.sum"), string(sum))
	}
	for i, sn := range snippets {
		pkg := fmt.Sprintf("s%03d", i+1)
		writeFile(t, filepath.Join(mod, pkg, "snippet.go"), sn.wrap(pkg))
	}

	// -gcflags=-e lists every error instead of the first ten of a package.
	cmd := exec.Command("go", "build", "-gcflags=-e", "./...")
	cmd.Dir = mod
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run()
	errs, other := parseBuildOutput(out.String())
	var problems []string
	for _, e := range errs {
		problems = append(problems, e.pos+": "+e.msg)
	}
	// go build also fails for the variables a fragment declares and does not use; those were filtered out.
	if len(other) > 0 || (runErr != nil && len(errs) == 0 && out.Len() == 0) {
		problems = append(problems, fmt.Sprintf("go build: %v\n%s", runErr, strings.Join(other, "\n")))
	}
	return problems
}

// TestSnippetsDetectErrors pins that the compile step is not vacuous: a wrong snippet is reported with its
// position in the Markdown file, and a variable that a fragment does not use is not.
func TestSnippetsDetectErrors(t *testing.T) {
	if os.Getenv("DOCCHECK_COMPILE") == "" {
		t.Skip("set DOCCHECK_COMPILE=1 to run")
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	md := "# T\n\n```go\nrep, err := jotaro.Run(ctx, jotaro.Plan{})\n```\n\n```go\njotaro.NoSuchFunc()\n```\n"
	sn, problems := snippetsOf(parseDocument("x.md", md))
	if len(problems) != 0 || len(sn) != 2 {
		t.Fatalf("snippets = %d, problems = %v", len(sn), problems)
	}
	got := buildSnippets(t, root, sn)
	if len(got) != 1 || !strings.HasPrefix(got[0], "x.md:8:") || !strings.Contains(got[0], "NoSuchFunc") {
		t.Errorf("problems = %q, want one for x.md:8 about NoSuchFunc", got)
	}
}

func TestSnippetsOf(t *testing.T) {
	md := strings.Join([]string{
		"# T",
		"<!-- doccheck: before var f *os.File -->",
		"",
		"<!-- doccheck: before type T struct{} -->",
		"```go",
		"f.Close()",
		"```",
		"text",
		"<!-- doccheck: skip: pseudo code -->",
		"```go",
		"nonsense",
		"```",
		"<!-- doccheck: fields jotaro.Plan -->",
		"```go",
		"Users: 1,",
		"```",
		"<!-- doccheck: stmts -->",
		"```go",
		"var x = 1",
		"```",
		"```go",
		"var y = 1",
		"```",
		"```go",
		"package main",
		"```",
		"```go title=x",
		"var z = 1",
		"```",
		"```sh",
		"echo hi",
		"```",
	}, "\n")
	sn, problems := snippetsOf(parseDocument("a.md", md))
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	var modes []snippetMode
	for _, s := range sn {
		modes = append(modes, s.mode)
	}
	want := []snippetMode{modeStatements, modeFields, modeStatements, modeDecls, modeFile, modeDecls}
	if !reflect.DeepEqual(modes, want) {
		t.Fatalf("modes = %v, want %v", modes, want)
	}
	if len(sn[0].before) != 2 || sn[0].before[1].line != 4 {
		t.Errorf("before = %+v, want the two comments above the first block", sn[0].before)
	}
	if sn[1].typ != "jotaro.Plan" {
		t.Errorf("typ = %q", sn[1].typ)
	}

	_, problems = snippetsOf(parseDocument("b.md", "<!-- doccheck: skip -->\n```go\nx\n```\n<!-- doccheck: nope -->\n```go\nx\n```\n<!-- doccheck: stmts -->\n```sh\nx\n```\n"))
	if len(problems) != 3 {
		t.Errorf("want 3 problems (skip without reason, unknown directive, comment before a non-go block), got %q", problems)
	}
}

func TestWrap(t *testing.T) {
	d := parseDocument("docs/a.md", "<!-- doccheck: before var f *os.File -->\n```go\nf.Close()\n```\n")
	sn, _ := snippetsOf(d)
	got := sn[0].wrap("s001")
	for _, want := range []string{"package s001\n", "func snippet(ctx context.Context) {", "//line docs/a.md:1:1\nvar f *os.File\n", "//line docs/a.md:3:1\nf.Close()\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("wrapped source lacks %q:\n%s", want, got)
		}
	}
	whole, _ := snippetsOf(parseDocument("a.md", "```go\npackage main\n\nfunc main() {}\n```\n"))
	if got := whole[0].wrap("s002"); strings.Contains(got, "import (") || !strings.HasPrefix(got, "//line a.md:2:1\npackage main") {
		t.Errorf("a whole file must be kept as it is:\n%s", got)
	}
}

func TestParseBuildOutput(t *testing.T) {
	out := "# doccheck.test/s001\r\ns001\\README.md:12:3: declared and not used: rep\n" +
		"s002\\docs\\result.md:5:9: undefined: x\n" +
		"go: downloading example.com/m v1.0.0\n" +
		"s003/a.md:7: declared and not used: err\n" +
		"go: updates to go.mod needed\n"
	errs, other := parseBuildOutput(out)
	if want := []compileError{{"docs/result.md:5:9", "undefined: x"}}; !reflect.DeepEqual(errs, want) {
		t.Errorf("errs = %+v, want %+v", errs, want)
	}
	if want := []string{"go: updates to go.mod needed"}; !reflect.DeepEqual(other, want) {
		t.Errorf("other = %q, want %q", other, want)
	}
}
