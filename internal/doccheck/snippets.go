package doccheck

import (
	"errors"
	"fmt"
	"strings"
)

// A Go snippet is a fenced block whose language is go. Most of them are fragments (statements, a field of
// a composite literal, a declaration), so each one is wrapped into a file of its own package before it is
// compiled; see wrap. Authors steer that with comments right before the block:
//
//	<!-- doccheck: skip: <reason> -->      do not compile the block (the reason is required)
//	<!-- doccheck: fields <Type> -->       the block is the body of a composite literal of <Type>
//	<!-- doccheck: stmts -->              the block is a list of statements even though it starts with var
//	<!-- doccheck: before <Go code> -->    declarations that the block relies on (a variable the text
//	                                       introduces earlier, a helper it leaves out); one comment per line
//
// Without a comment the mode is guessed from the first line: "package" is a whole file, "var", "type",
// "func" or "const" are top-level declarations, anything else is a list of statements that runs in a
// function with a context.Context named ctx.

// importBlock is the fixed import block of a wrapped snippet. Every name is referenced in the file so an
// import that a snippet does not use is not an error. Add a package here when a snippet needs it.
const importBlock = `import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/record"
	"github.com/RIRUa/jotaro/report"
)

var (
	_ context.Context
	_ = errors.New
	_ = fmt.Sprint
	_ io.Reader
	_ = log.Println
	_ = http.StatusOK
	_ = os.Exit
	_ time.Duration
	_ jotaro.Plan
	_ record.Record
	_ report.Report
)
`

// snippetMode says how a snippet is wrapped.
type snippetMode int

const (
	modeStatements snippetMode = iota // a function body
	modeDecls                         // top-level declarations
	modeFile                          // a whole file ("package ...")
	modeFields                        // the body of a composite literal
)

// snippet is a Go block to compile.
type snippet struct {
	doc    string // the document, slash-separated
	block  codeBlock
	mode   snippetMode
	typ    string      // modeFields: the literal's type
	before []directive // setup declarations
}

// snippetsSkipped lists the directories whose Markdown files are not compiled: the documents embedded in the
// MCP server are served to models as they are, so they carry no doccheck comments, and guide_test.go
// compiles the skeleton that matters there.
var snippetsSkipped = []string{"internal/mcpserver/content/"}

// snippetsOf returns the snippets of a document, and the problems with its doccheck comments. A skipped
// block is left out.
func snippetsOf(d *document) (out []snippet, problems []string) {
	for _, b := range d.blocks {
		sn := snippet{doc: d.path, block: b}
		skip, stmts := false, false
		for _, dir := range b.directives {
			where := fmt.Sprintf("%s:%d", d.path, dir.line)
			verb, arg, _ := strings.Cut(dir.text, " ")
			arg = strings.TrimSpace(arg)
			switch {
			case b.lang() != "go":
				problems = append(problems, where+": a doccheck comment belongs before a go block")
			case strings.HasPrefix(dir.text, "skip"):
				if reason := strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(dir.text, "skip")), ":"); strings.TrimSpace(reason) == "" {
					problems = append(problems, where+": skip needs a reason: <!-- doccheck: skip: why -->")
				}
				skip = true
			case verb == "stmts" && arg == "":
				stmts = true
			case verb == "fields" && arg != "":
				sn.mode, sn.typ = modeFields, arg
			case verb == "before" && arg != "":
				sn.before = append(sn.before, directive{text: arg, line: dir.line})
			default:
				problems = append(problems, fmt.Sprintf("%s: unknown doccheck directive %q (want skip: <reason>, fields <Type> or before <code>)", where, dir.text))
			}
		}
		if b.lang() != "go" || skip {
			continue
		}
		if sn.mode != modeFields {
			sn.mode = guessMode(b.code)
			if stmts && sn.mode == modeDecls {
				sn.mode = modeStatements
			}
		}
		out = append(out, sn)
	}
	return out, problems
}

// guessMode looks at the first line of code that is not a comment.
func guessMode(code string) snippetMode {
	for _, l := range strings.Split(code, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "//") {
			continue
		}
		word, _, _ := strings.Cut(l, " ")
		switch word {
		case "package":
			return modeFile
		case "var", "type", "func", "const":
			return modeDecls
		}
		return modeStatements
	}
	return modeStatements
}

// wrap returns the Go source that makes the snippet compilable under the package name pkg. //line
// directives make the compiler report positions in the Markdown file.
func (s snippet) wrap(pkg string) string {
	var b strings.Builder
	line := func(l directive) { fmt.Fprintf(&b, "//line %s:%d:1\n%s\n", s.doc, l.line, l.text) }
	body := func() {
		fmt.Fprintf(&b, "//line %s:%d:1\n%s\n", s.doc, s.block.line+1, s.block.code)
	}
	if s.mode == modeFile {
		body()
		return b.String()
	}
	fmt.Fprintf(&b, "package %s\n\n%s\n", pkg, importBlock)
	switch s.mode {
	case modeDecls:
		for _, l := range s.before {
			line(l)
		}
		body()
	case modeFields:
		for _, l := range s.before {
			line(l)
		}
		fmt.Fprintf(&b, "var _ = %s{\n", s.typ)
		body()
		b.WriteString("}\n")
	default:
		b.WriteString("func snippet(ctx context.Context) {\n\t_ = ctx\n")
		for _, l := range s.before {
			line(l)
		}
		body()
		b.WriteString("}\n")
	}
	return b.String()
}

// compileError is one line of compiler output that points into a Markdown file.
type compileError struct {
	pos, msg string
}

// parseBuildOutput splits the output of go build into the errors that matter. A snippet is a fragment, so a
// variable that it declares and does not use is not an error. It returns the lines it could not read as an
// error too (a module problem, for example), so nothing is lost.
func parseBuildOutput(out string) (errs []compileError, other []string) {
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "# ") || strings.HasPrefix(l, "go: downloading ") || strings.HasPrefix(l, "go: finding ") {
			continue
		}
		pos, msg, ok := splitPosition(l)
		if !ok {
			other = append(other, l)
			continue
		}
		if strings.HasPrefix(msg, "declared and not used") || strings.HasSuffix(msg, "declared and not used") {
			continue
		}
		// go build prints the package directory (s012/) before the file name.
		pos = strings.ReplaceAll(pos, `\`, "/") // Windows prints s001\README.md
		if len(pos) > 5 && pos[0] == 's' && pos[4] == '/' {
			pos = pos[5:]
		}
		errs = append(errs, compileError{pos, msg})
	}
	return errs, other
}

// splitPosition splits "file.md:12:3: message" (the column is optional).
func splitPosition(l string) (pos, msg string, ok bool) {
	i := strings.Index(l, ".md:")
	if i < 0 {
		return "", "", false
	}
	rest := l[i+len(".md:"):]
	j := 0
	for j < len(rest) && (rest[j] >= '0' && rest[j] <= '9' || rest[j] == ':') {
		j++
	}
	if j == 0 || !strings.HasPrefix(rest[j:], " ") {
		return "", "", false
	}
	return strings.TrimSuffix(l[:i+len(".md:")+j], ":"), strings.TrimSpace(rest[j:]), true
}

var errNoSnippets = errors.New("no Go snippets found in the documentation")
