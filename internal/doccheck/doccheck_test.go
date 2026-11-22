package doccheck

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the repository root, two levels above this package.
var repoRoot = filepath.Join("..", "..")

// TestParity pins that every English document and its Japanese twin have the same structure. The documents
// listed in parity_allowlist.txt are allowed to lag behind, check by check.
func TestParity(t *testing.T) {
	b, err := os.ReadFile("parity_allowlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	allow, err := parseAllowlist(string(b))
	if err != nil {
		t.Fatalf("parity_allowlist.txt: %v", err)
	}
	findings, err := checkParity(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	remaining, stale := applyAllowlist(findings, allow)
	for _, f := range remaining {
		t.Errorf("%s [%s]: %s", f.file, f.check, f.msg)
	}
	for _, a := range stale {
		t.Errorf("parity_allowlist.txt:%d: %s [%s] no longer differs; remove the entry", a.line, a.file, a.check)
	}
}

func TestParseDocument(t *testing.T) {
	text := "# Title\r\n\r\n```sh\r\n# not a heading\r\nls\r\n```\r\n\r\n## Two ##\r\n\r\n   ~~~~go\r\n```\r\nx := 1\r\n~~~~\r\n\r\n####### seven\r\n#nospace\r\n    # indented code\r\n### Three\r\n\r\n```\r\nunterminated"
	d := parseDocument("a.md", text)
	var hs []string
	for _, h := range d.headings {
		hs = append(hs, strings.Repeat("#", h.level)+" "+h.text)
	}
	if got, want := strings.Join(hs, "|"), "# Title|## Two|### Three"; got != want {
		t.Errorf("headings = %q, want %q", got, want)
	}
	if len(d.blocks) != 3 {
		t.Fatalf("got %d blocks, want 3: %+v", len(d.blocks), d.blocks)
	}
	if b := d.blocks[0]; b.info != "sh" || b.code != "# not a heading\nls" || b.line != 3 {
		t.Errorf("block 0 = %+v", b)
	}
	if b := d.blocks[1]; b.info != "go" || b.code != "```\nx := 1" {
		t.Errorf("block 1 = %+v (a shorter fence of another kind must not close it)", b)
	}
	if b := d.blocks[2]; b.info != "" || b.code != "unterminated" {
		t.Errorf("block 2 = %+v", b)
	}
}

func TestCompare(t *testing.T) {
	en := parseDocument("a.md", "# A\n\n## B\n\n```go\nx := 1 // one\ny := 2\n```\n\n```\nflow: the words\n```\n")
	same := parseDocument("a_jp.md", "# あ\n\n## い\n\n```go\nx := 1 // いち\n\ny := 2   \n```\n\n```\nフロー: 単語\n```\n")
	if got := compare(en, same); len(got) != 0 {
		t.Errorf("translated comments and diagram words must not be reported, got %+v", got)
	}

	cases := []struct {
		name, jp string
		want     map[string]bool
	}{
		{"missing heading", "# あ\n\n```go\nx := 1\ny := 2\n```\n\n```\nx\n```\n", map[string]bool{checkHeadings: true}},
		{"heading level", "# あ\n\n### い\n\n```go\nx := 1\ny := 2\n```\n\n```\nx\n```\n", map[string]bool{checkHeadings: true}},
		{"missing block", "# あ\n\n## い\n\n```go\nx := 1\ny := 2\n```\n", map[string]bool{checkBlocks: true}},
		{"language", "# あ\n\n## い\n\n```sh\nx := 1\ny := 2\n```\n\n```\nx\n```\n", map[string]bool{checkBlocks: true}},
		{"code", "# あ\n\n## い\n\n```go\nx := 1\ny := 3\n```\n\n```\nx\n```\n", map[string]bool{checkCode: true}},
		{"diagram lines", "# あ\n\n## い\n\n```go\nx := 1\ny := 2\n```\n\n```\nx\ny\n```\n", map[string]bool{checkCode: true}},
	}
	for _, tc := range cases {
		got := map[string]bool{}
		for _, f := range compare(en, parseDocument("a_jp.md", tc.jp)) {
			got[f.check] = true
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: reported checks %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCheckParityTree(t *testing.T) {
	root := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# A\n")
	write("README_jp.md", "# あ\n")
	write("DESIGN.md", "# A\n")                  // required, no twin
	write("CONTRIBUTING.md", "# A\n")            // English only is fine
	write("docs/new.md", "# A\n")                // required below docs/, no twin
	write("docs/old_jp.md", "# あ\n")             // twin without English
	write("examples/x/README.md", "# A\n## B\n") // required, twin differs
	write("examples/x/README_jp.md", "# あ\n")
	write("testdata/skip.md", "# A\n")            // not looked at
	write(".github/PULL_REQUEST_TEMPLATE.md", "") // hidden directory, not looked at

	got, err := checkParity(root)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, f := range got {
		lines = append(lines, f.file+" "+f.check)
	}
	sort.Strings(lines)
	want := []string{"DESIGN.md twin", "docs/new.md twin", "docs/old.md twin", "examples/x/README.md headings"}
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("findings = %q, want %q", lines, want)
	}
}

func TestAllowlist(t *testing.T) {
	allow, err := parseAllowlist("# comment\n\ndocs/ci.md | twin | follows in #1\r\nREADME.md|code|a | b\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(allow) != 2 || allow[1].reason != "a | b" || allow[1].line != 4 {
		t.Fatalf("parsed %+v", allow)
	}
	remaining, stale := applyAllowlist([]finding{{"docs/ci.md", checkTwin, "x"}, {"docs/ci.md", checkCode, "y"}}, allow)
	if len(remaining) != 1 || remaining[0].check != checkCode {
		t.Errorf("remaining = %+v", remaining)
	}
	if len(stale) != 1 || stale[0].file != "README.md" {
		t.Errorf("stale = %+v", stale)
	}
	for _, bad := range []string{"README.md | twin", "README.md | twin |  ", "README.md | other | why"} {
		if _, err := parseAllowlist(bad); err == nil {
			t.Errorf("parseAllowlist(%q) succeeded", bad)
		}
	}
}
