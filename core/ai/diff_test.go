package ai

import (
	"strings"
	"testing"
)

func TestUnifiedDiff(t *testing.T) {
	diffNew := UnifiedDiff("/etc/app.conf", "", "port=8080\nhost=0.0.0.0\n", 3)
	if !strings.Contains(diffNew, "--- /dev/null") || !strings.Contains(diffNew, "+++ b//etc/app.conf") || !strings.Contains(diffNew, "+port=8080") {
		t.Fatalf("unexpected new file diff:\n%s", diffNew)
	}

	before := "line1\nline2\nline3\nline4\n"
	after := "line1\nline2-updated\nline3\nline4\nline5\n"
	diffEdit := UnifiedDiff("app.conf", before, after, 1)
	if !strings.Contains(diffEdit, "-line2") || !strings.Contains(diffEdit, "+line2-updated") || !strings.Contains(diffEdit, "+line5") {
		t.Fatalf("unexpected edit diff:\n%s", diffEdit)
	}
	if UnifiedDiff("same.txt", "a\nb\n", "a\nb\n", 3) != "" {
		t.Fatal("expected empty diff when contents match")
	}
}
