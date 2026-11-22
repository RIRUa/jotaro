package doccheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// checklistItem matches the first line of an item of the "before you believe a result" list: "5. **Title.** ...".
var checklistItem = regexp.MustCompile(`^(\d+)\. \*\*`)

// sectionItems returns the numbered items 1 to last of the section that starts at the "## heading" line of text and
// runs to the next "## " heading, keyed by their number. Items are one line each. ok is false when the heading is missing.
func sectionItems(text, heading string, last int) (items map[int]string, ok bool) {
	items = map[int]string{}
	in := false
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "## ") {
			in = strings.TrimSpace(strings.TrimPrefix(line, "## ")) == heading
			ok = ok || in
			continue
		}
		if !in {
			continue
		}
		m := checklistItem.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil && n >= 1 && n <= last {
			items[n] = line
		}
	}
	return items, ok
}

// TestTuningChecklistMatchesMCPGuide pins that items 1 to 7 of "Before believing a result" in docs/tuning.md
// are copies of the checklist that the MCP resource jotaro://result serves under "Reading a result"
// (internal/mcpserver/content/result.md), so that an assistant and a person read the same thing. The Japanese twin
// is a translation and is covered by the parity check only.
func TestTuningChecklistMatchesMCPGuide(t *testing.T) {
	const last = 7
	read := func(path, heading string) map[int]string {
		b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		items, ok := sectionItems(string(b), heading, last)
		if !ok {
			t.Fatalf("%s has no %q section", path, "## "+heading)
		}
		return items
	}
	mcp := read("internal/mcpserver/content/result.md", "Reading a result")
	tuning := read("docs/tuning.md", "Before believing a result")
	for n := 1; n <= last; n++ {
		if mcp[n] == "" {
			t.Fatalf("internal/mcpserver/content/result.md has no checklist item %d", n)
		}
		if tuning[n] != mcp[n] {
			t.Errorf("docs/tuning.md item %d differs from internal/mcpserver/content/result.md:\n tuning: %s\n mcp:    %s", n, tuning[n], mcp[n])
		}
	}
}
