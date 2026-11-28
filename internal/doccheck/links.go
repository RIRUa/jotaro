package doccheck

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// link is a link or image target found in the text of a document.
type link struct {
	target string
	line   int
}

var (
	// [text](target) and ![alt](target); the target may be <wrapped> and may be followed by a "title".
	inlineLinkRE = regexp.MustCompile(`!?\[[^\]]*\]\(\s*(<[^>]*>|[^)\s]*)(?:\s+(?:"[^"]*"|'[^']*'))?\s*\)`)
	// [label]: target
	refDefRE = regexp.MustCompile(`^\s{0,3}\[[^\]]+\]:\s*(<[^>]*>|\S+)`)
	// href="target" and src="target" in inline HTML
	htmlLinkRE   = regexp.MustCompile(`(?i)\b(?:href|src)\s*=\s*"([^"]*)"`)
	htmlAnchorRE = regexp.MustCompile(`(?i)<a\s[^>]*\b(?:id|name)\s*=\s*"([^"]+)"`)
	tagRE        = regexp.MustCompile(`<[^>]*>`)
	mdLinkRE     = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
)

// linksIn returns the link targets on one line of prose. Code spans are blanked first, so "[x](y)" in
// backticks is not a link.
func linksIn(line string, n int) []link {
	line = stripCodeSpans(line)
	var out []link
	add := func(t string) {
		t = strings.TrimSuffix(strings.TrimPrefix(t, "<"), ">")
		out = append(out, link{target: t, line: n})
	}
	for _, m := range inlineLinkRE.FindAllStringSubmatch(line, -1) {
		add(m[1])
	}
	if m := refDefRE.FindStringSubmatch(line); m != nil {
		add(m[1])
	}
	for _, m := range htmlLinkRE.FindAllStringSubmatch(line, -1) {
		add(m[1])
	}
	return out
}

// stripCodeSpans removes `code spans` (a run of n backticks up to the next run of exactly n). A run that is
// never closed is kept as text.
func stripCodeSpans(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '`' {
			b.WriteByte(s[i])
			i++
			continue
		}
		n := 0
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		end := -1
		for j := i + n; j < len(s); {
			if s[j] != '`' {
				j++
				continue
			}
			m := 0
			for j+m < len(s) && s[j+m] == '`' {
				m++
			}
			if m == n {
				end = j + m
				break
			}
			j += m
		}
		if end < 0 {
			b.WriteString(s[i : i+n])
			i += n
			continue
		}
		i = end
	}
	return b.String()
}

func htmlAnchorsIn(line string) []string {
	var out []string
	for _, m := range htmlAnchorRE.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

// slug turns heading text into the anchor GitHub gives it: markup is dropped, the text is lower-cased,
// everything but letters, digits, "_" and "-" is removed and spaces become "-".
func slug(text string) string {
	text = mdLinkRE.ReplaceAllString(text, "$1")
	text = tagRE.ReplaceAllString(text, "")
	text = strings.NewReplacer("`", "", "*", "").Replace(text)
	var b strings.Builder
	for _, r := range strings.ToLower(text) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// anchorSet returns the anchors a document offers: its headings (a repeated heading gets -1, -2, ... as on
// GitHub) and its HTML anchors.
func (d *document) anchorSet() map[string]bool {
	set := map[string]bool{}
	seen := map[string]int{}
	for _, h := range d.headings {
		s := slug(h.text)
		a := s
		for n := seen[s]; set[a] || n > 0 && a == s; n++ {
			seen[s] = n + 1
			a = fmt.Sprintf("%s-%d", s, n+1)
		}
		set[a] = true
	}
	for _, a := range d.anchors {
		set[strings.ToLower(a)] = true // fragments are lower-cased before the lookup
	}
	return set
}

// linkProblem is a link that does not resolve.
type linkProblem struct {
	file string
	line int
	msg  string
}

func (p linkProblem) String() string { return fmt.Sprintf("%s:%d: %s", p.file, p.line, p.msg) }

// checkLinks resolves every relative link of every Markdown file: the target must exist (a file or a
// directory), and when it has a #fragment and is a Markdown file the fragment must be one of its anchors.
// Links with a scheme (https:, mailto:, ...) are not followed: the check is offline.
func checkLinks(root string) ([]linkProblem, error) {
	files, err := markdownFiles(root)
	if err != nil {
		return nil, err
	}
	docs := map[string]*document{}
	load := func(p string) (*document, error) {
		if d, ok := docs[p]; ok {
			return d, nil
		}
		d, err := readDocument(root, p)
		if err != nil {
			return nil, err
		}
		docs[p] = d
		return d, nil
	}
	var out []linkProblem
	for _, f := range files {
		d, err := load(f)
		if err != nil {
			return nil, err
		}
		for _, l := range d.links {
			target, frag, skip := splitTarget(l.target)
			if skip {
				continue
			}
			add := func(format string, args ...any) {
				out = append(out, linkProblem{f, l.line, fmt.Sprintf(format, args...)})
			}
			dest := f
			if target != "" {
				if strings.HasPrefix(target, "/") {
					dest = path.Clean(strings.TrimPrefix(target, "/"))
				} else {
					dest = path.Join(path.Dir(f), target)
				}
				if dest == ".." || strings.HasPrefix(dest, "../") {
					add("link %q leaves the repository", l.target)
					continue
				}
				info, err := os.Stat(filepath.Join(root, filepath.FromSlash(dest)))
				if err != nil {
					add("link %q: %s does not exist", l.target, dest)
					continue
				}
				if info.IsDir() || !strings.HasSuffix(dest, ".md") {
					continue // a #fragment on a directory or on a source file (#L10) is not checked
				}
			}
			if frag == "" {
				continue
			}
			td, err := load(dest)
			if err != nil {
				return nil, err
			}
			if !td.anchorSet()[frag] {
				add("link %q: %s has no heading or anchor #%s", l.target, dest, frag)
			}
		}
	}
	return out, nil
}

// splitTarget splits a link target into its path and fragment. skip is true for targets that are not
// relative references: a URL with a scheme, or a protocol-relative one. The path has its query removed and
// is percent-decoded; the fragment is lower-cased and decoded, as GitHub anchors are.
func splitTarget(t string) (p, frag string, skip bool) {
	t = strings.TrimSpace(t)
	if t == "" || strings.HasPrefix(t, "//") {
		return "", "", true
	}
	if i := strings.IndexAny(t, ":/?#"); i > 0 && t[i] == ':' {
		return "", "", true // a scheme such as https: or mailto:
	}
	p, frag, _ = strings.Cut(t, "#")
	p, _, _ = strings.Cut(p, "?")
	if dp, err := url.PathUnescape(p); err == nil {
		p = dp
	}
	if df, err := url.PathUnescape(frag); err == nil {
		frag = df
	}
	return p, strings.ToLower(frag), false
}
