package doccheck

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// The checks of a pair of documents. An allowlist entry names one of them.
const (
	checkTwin     = "twin"     // the Japanese file exists
	checkHeadings = "headings" // same number and levels of headings, in the same order
	checkBlocks   = "blocks"   // same number of fenced code blocks, with the same info strings
	checkCode     = "code"     // the same code in the code blocks (comments and diagrams may be translated)
)

var allChecks = []string{checkTwin, checkHeadings, checkBlocks, checkCode}

// jpSuffix turns "docs/result.md" into "docs/result_jp.md".
const jpSuffix = "_jp.md"

// finding is one parity problem.
type finding struct {
	file  string // the English file the allowlist entry names
	check string
	msg   string
}

// twinRequired reports whether the English document p must have a Japanese twin. The rule is by location, so
// that a new document is covered without editing a list: the README and DESIGN at the root, and every
// Markdown file below docs/ and examples/. The other root documents (CONTRIBUTING, CHANGELOG, ...) are
// English only.
func twinRequired(p string) bool {
	switch {
	case p == "README.md" || p == "DESIGN.md":
		return true
	case strings.HasPrefix(p, "docs/") || strings.HasPrefix(p, "examples/"):
		return true
	}
	return false
}

// markdownFiles lists the Markdown files of the repository as slash-separated paths relative to root. It
// skips hidden directories, vendored code and test data.
func markdownFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(name, ".md") {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// jpTwin returns the path of the Japanese counterpart of the English document p.
func jpTwin(p string) string { return strings.TrimSuffix(p, ".md") + jpSuffix }

// checkParity compares every English document with its Japanese twin and returns what differs.
func checkParity(root string) ([]finding, error) {
	files, err := markdownFiles(root)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(files))
	for _, f := range files {
		set[f] = true
	}
	var out []finding
	for _, f := range files {
		if strings.HasSuffix(f, jpSuffix) {
			if en := strings.TrimSuffix(f, jpSuffix) + ".md"; !set[en] {
				out = append(out, finding{en, checkTwin, fmt.Sprintf("%s has no English counterpart %s", f, en)})
			}
			continue
		}
		jp := jpTwin(f)
		if !set[jp] {
			if twinRequired(f) {
				out = append(out, finding{f, checkTwin, fmt.Sprintf("%s has no Japanese counterpart %s", f, jp)})
			}
			continue
		}
		en, err := readDocument(root, f)
		if err != nil {
			return nil, err
		}
		ja, err := readDocument(root, jp)
		if err != nil {
			return nil, err
		}
		out = append(out, compare(en, ja)...)
	}
	return out, nil
}

// compare returns the differences between an English document and its Japanese twin.
func compare(en, ja *document) []finding {
	var out []finding
	add := func(check, format string, args ...any) {
		out = append(out, finding{en.path, check, fmt.Sprintf(format, args...)})
	}

	if len(en.headings) != len(ja.headings) {
		add(checkHeadings, "%s has %d headings, %s has %d", en.path, len(en.headings), ja.path, len(ja.headings))
	}
	for i := 0; i < min(len(en.headings), len(ja.headings)); i++ {
		e, j := en.headings[i], ja.headings[i]
		if e.level != j.level {
			add(checkHeadings, "heading %d: %s:%d is level %d (%q), %s:%d is level %d (%q); the headings no longer line up from here",
				i+1, en.path, e.line, e.level, e.text, ja.path, j.line, j.level, j.text)
			break
		}
	}

	if len(en.blocks) != len(ja.blocks) {
		add(checkBlocks, "%s has %d fenced code blocks, %s has %d", en.path, len(en.blocks), ja.path, len(ja.blocks))
	}
	for i := 0; i < min(len(en.blocks), len(ja.blocks)); i++ {
		e, j := en.blocks[i], ja.blocks[i]
		if e.info != j.info {
			add(checkBlocks, "code block %d: %s:%d is ```%s, %s:%d is ```%s", i+1, en.path, e.line, e.info, ja.path, j.line, j.info)
		}
		if a, b := codeForCompare(e), codeForCompare(j); a != b {
			add(checkCode, "code block %d differs: %s:%d and %s:%d (%s)", i+1, en.path, e.line, ja.path, j.line, firstDiff(a, b))
		}
	}
	return out
}

// codeForCompare returns the part of a code block that must be the same in both languages. Comments are
// translated, so they are dropped from blocks whose language is known (// for go, # for shell and the like),
// and so are blank lines and trailing spaces. A block without a language is a diagram or a usage line whose
// words are translated; only its number of lines is compared.
func codeForCompare(b codeBlock) string {
	lines := strings.Split(b.code, "\n")
	var marker string
	switch b.info {
	case "go", "golang":
		marker = "//"
	case "sh", "bash", "shell", "console", "yaml", "yml", "toml":
		marker = "#"
	case "":
		return fmt.Sprintf("%d lines", len(lines))
	}
	var out []string
	for _, l := range lines {
		if marker != "" {
			l = stripComment(l, marker)
		}
		if l = strings.TrimRight(l, " \t"); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// stripComment cuts a trailing comment that starts with marker at the start of the line or after a space
// ("https://" is not a comment). It does not look for quotes, which is enough for the snippets in the docs.
func stripComment(line, marker string) string {
	for i := 0; i+len(marker) <= len(line); i++ {
		if strings.HasPrefix(line[i:], marker) && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t') {
			return line[:i]
		}
	}
	return line
}

// firstDiff describes the first line where two code blocks differ.
func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < min(len(al), len(bl)); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("line %d: %q vs %q", i+1, al[i], bl[i])
		}
	}
	return fmt.Sprintf("%d lines vs %d lines", len(al), len(bl))
}

// allowEntry is one line of the allowlist: "<english file> | <check> | <reason>".
type allowEntry struct {
	file, check, reason string
	line                int
}

// parseAllowlist reads the allowlist. Blank lines and lines starting with # are ignored. Every entry needs a
// reason, and the check is one of twin, headings, blocks, code.
func parseAllowlist(text string) ([]allowEntry, error) {
	var out []allowEntry
	for i, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("line %d: want \"<file> | <check> | <reason>\", got %q", i+1, line)
		}
		e := allowEntry{
			file:   path.Clean(strings.TrimSpace(parts[0])),
			check:  strings.TrimSpace(parts[1]),
			reason: strings.TrimSpace(parts[2]),
			line:   i + 1,
		}
		if e.reason == "" {
			return nil, fmt.Errorf("line %d: the entry for %s %s has no reason", i+1, e.file, e.check)
		}
		known := false
		for _, c := range allChecks {
			known = known || c == e.check
		}
		if !known {
			return nil, fmt.Errorf("line %d: unknown check %q (want one of %s)", i+1, e.check, strings.Join(allChecks, ", "))
		}
		out = append(out, e)
	}
	return out, nil
}

// applyAllowlist removes the findings the allowlist covers. It also returns a problem for every entry that
// covers nothing, so the list shrinks as the Japanese documents catch up.
func applyAllowlist(findings []finding, allow []allowEntry) (remaining []finding, stale []allowEntry) {
	used := make([]bool, len(allow))
	for _, f := range findings {
		covered := false
		for i, a := range allow {
			if a.file == f.file && a.check == f.check {
				used[i], covered = true, true
			}
		}
		if !covered {
			remaining = append(remaining, f)
		}
	}
	for i, a := range allow {
		if !used[i] {
			stale = append(stale, a)
		}
	}
	return remaining, stale
}
