package report

import (
	"strings"
	"testing"
)

func TestPrintWarnsOnDroppedArrivals(t *testing.T) {
	rep := &Report{Open: true, Dropped: 3}
	var b strings.Builder
	rep.Print(&b)
	if !strings.Contains(b.String(), "3 arrivals were dropped") {
		t.Errorf("missing drop warning:\n%s", b.String())
	}

	rep.Dropped = 0
	b.Reset()
	rep.Print(&b)
	if strings.Contains(b.String(), "arrivals were dropped") {
		t.Errorf("unexpected drop warning:\n%s", b.String())
	}
}
