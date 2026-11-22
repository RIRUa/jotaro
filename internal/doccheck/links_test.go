package doccheck

import (
	"reflect"
	"sort"
	"testing"
)

func TestLinksIn(t *testing.T) {
	line := "[a](a.md) ![img](img/x.png \"title\") `[no](no.md)` ``[no2](`no2.md`)`` [b](<b c.md#h>) <a href=\"h.md\">x</a> [url](https://example.com/a_(b))"
	var got []string
	for _, l := range linksIn(line, 3) {
		got = append(got, l.target)
	}
	want := []string{"a.md", "img/x.png", "b c.md#h", "https://example.com/a_(b", "h.md"}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("targets = %q, want %q", got, want)
	}
	if got := linksIn("[0.1.4]: https://example.com/compare/v0.1.3...v0.1.4", 1); len(got) != 1 || got[0].target != "https://example.com/compare/v0.1.3...v0.1.4" {
		t.Errorf("reference definition: %+v", got)
	}
	if got := stripCodeSpans("a `b` c ` d"); got != "a  c ` d" {
		t.Errorf("stripCodeSpans = %q", got)
	}
}

func TestSlug(t *testing.T) {
	for text, want := range map[string]string{
		"Threshold syntax":                 "threshold-syntax",
		"`Plan.AbortOn` and [links](x.md)": "planaborton-and-links",
		"Run, check, read":                 "run-check-read",
		"結果（JSON）の読み方":                     "結果jsonの読み方",
		"a_b - c":                          "a_b---c",
		"<b>Bold</b> *it*":                 "bold-it",
	} {
		if got := slug(text); got != want {
			t.Errorf("slug(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestSplitTarget(t *testing.T) {
	for _, tc := range []struct {
		in, path, frag string
		skip           bool
	}{
		{"docs/result.md", "docs/result.md", "", false},
		{"docs/result.md#Threshold-Syntax", "docs/result.md", "threshold-syntax", false},
		{"#top", "", "top", false},
		{"a%20b.md?x=1#h", "a b.md", "h", false},
		{"https://example.com/a#b", "", "", true},
		{"mailto:a@example.com", "", "", true},
		{"//example.com/x", "", "", true},
		{"", "", "", true},
		{"dir/a:b.md", "dir/a:b.md", "", false},
	} {
		p, frag, skip := splitTarget(tc.in)
		if p != tc.path || frag != tc.frag || skip != tc.skip {
			t.Errorf("splitTarget(%q) = %q, %q, %v; want %q, %q, %v", tc.in, p, frag, skip, tc.path, tc.frag, tc.skip)
		}
	}
}

func TestAnchorSet(t *testing.T) {
	d := parseDocument("a.md", "# A\n## Same\n## Same\n## Same\n<a id=\"Custom\"></a>\n```\n## not a heading\n```\n<!--\n## in a comment\n[x](missing.md)\n-->\n## foo\n## foo-1\n## foo\n")
	got := d.anchorSet()
	for _, want := range []string{"a", "same", "same-1", "same-2", "custom", "foo", "foo-1", "foo-2"} {
		if !got[want] {
			t.Errorf("missing anchor %q in %v", want, got)
		}
	}
	if len(got) != 8 || len(d.links) != 0 {
		t.Errorf("anchors = %v", got)
	}
}

func TestCheckLinksTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/README.md", "# Top\n\n[ok](docs/a.md#section-one) [ok2](#top) [dir](docs) [root](/docs/a.md) [src](x.go#L3)\n"+
		"[gone](docs/b.md) [anchor](docs/a.md#nope) [self](#nope) [out](../x.md) [ext](https://example.com/404)\n"+
		"```md\n[in code](missing.md)\n```\n"+"`[in span](missing.md)`\n")
	writeFile(t, root+"/x.go", "package x\n")
	writeFile(t, root+"/docs/a.md", "# A\n\n## Section one\n\n[up](../README.md#top) [sibling](a_jp.md)\n")
	writeFile(t, root+"/docs/a_jp.md", "# あ\n")
	got, err := checkLinks(root)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, p := range got {
		lines = append(lines, p.String())
	}
	want := []string{
		`README.md:4: link "../x.md" leaves the repository`,
		`README.md:4: link "#nope": README.md has no heading or anchor #nope`,
		`README.md:4: link "docs/a.md#nope": docs/a.md has no heading or anchor #nope`,
		`README.md:4: link "docs/b.md": docs/b.md does not exist`,
	}
	sort.Strings(lines)
	sort.Strings(want)
	if !reflect.DeepEqual(lines, want) {
		t.Errorf("problems:\n%q\nwant:\n%q", lines, want)
	}
}
