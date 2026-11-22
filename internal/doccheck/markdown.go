package doccheck

import (
	"os"
	"path/filepath"
	"strings"
)

// heading is an ATX heading ("## Title") outside of fenced code.
type heading struct {
	level int
	text  string
	line  int
}

// codeBlock is a fenced code block.
type codeBlock struct {
	info string // the info string after the opening fence, trimmed ("go", "sh", "")
	code string // the content, with "\n" line ends
	line int    // the line of the opening fence
}

// document is what the checks need to know about one Markdown file.
type document struct {
	path     string // slash-separated, relative to the repository root
	headings []heading
	blocks   []codeBlock
}

// readDocument reads and parses the Markdown file at root/path.
func readDocument(root, path string) (*document, error) {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return nil, err
	}
	return parseDocument(path, string(b)), nil
}

// parseDocument extracts headings and fenced code blocks. It follows the CommonMark rules that matter here:
// a fence is three or more backticks or tildes (up to three spaces of indent are not required here, since
// fences inside list items are indented further), it is closed by a fence of the same character that is at
// least as long, and an unterminated block runs to the end of the file. CRLF line ends are accepted.
func parseDocument(path, text string) *document {
	d := &document{path: path}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var (
		in      bool
		fenceCh byte
		fenceN  int
		cur     codeBlock
		body    []string
	)
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if in {
			if ch, n, rest := fence(trimmed); n >= fenceN && ch == fenceCh && strings.TrimSpace(rest) == "" {
				cur.code = strings.Join(body, "\n")
				d.blocks = append(d.blocks, cur)
				in = false
				continue
			}
			body = append(body, line)
			continue
		}
		if ch, n, rest := fence(trimmed); n >= 3 {
			in, fenceCh, fenceN = true, ch, n
			cur = codeBlock{info: strings.TrimSpace(rest), line: i + 1}
			body = body[:0]
			continue
		}
		if level, text, ok := atxHeading(line); ok {
			d.headings = append(d.headings, heading{level: level, text: text, line: i + 1})
		}
	}
	if in {
		cur.code = strings.Join(body, "\n")
		d.blocks = append(d.blocks, cur)
	}
	return d
}

// fence reports the fence character and length at the start of s (n is 0 when s does not start with one)
// and what follows the fence.
func fence(s string) (ch byte, n int, rest string) {
	if s == "" || (s[0] != '`' && s[0] != '~') {
		return 0, 0, s
	}
	ch = s[0]
	for n < len(s) && s[n] == ch {
		n++
	}
	return ch, n, s[n:]
}

// atxHeading parses "## Title" (up to three leading spaces, one to six #, then a space) and drops the
// optional closing #s.
func atxHeading(line string) (level int, text string, ok bool) {
	s := strings.TrimLeft(line, " ")
	if len(line)-len(s) > 3 {
		return 0, "", false
	}
	for level < len(s) && s[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(s) || (s[level] != ' ' && s[level] != '\t') {
		return 0, "", false
	}
	text = strings.TrimSpace(s[level:])
	if t := strings.TrimRight(text, "#"); t != text && (t == "" || t[len(t)-1] == ' ') {
		text = strings.TrimSpace(t)
	}
	return level, text, true
}
