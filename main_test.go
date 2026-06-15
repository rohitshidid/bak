package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestUnifiedDiffDefaultsToCompactHunks(t *testing.T) {
	var before, after strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&before, "line %02d\n", i)
		if i == 20 {
			fmt.Fprintln(&after, "line 20 changed")
			continue
		}
		fmt.Fprintf(&after, "line %02d\n", i)
	}

	got := unifiedDiff(before.String(), after.String(), false)
	if !strings.Contains(got, "@@ -17,7 +17,7 @@") {
		t.Fatalf("compact diff should include a focused hunk around line 20:\n%s", got)
	}
	if strings.Contains(got, "line 01") || strings.Contains(got, "line 30") {
		t.Fatalf("compact diff should not print unchanged distant lines:\n%s", got)
	}
	if !strings.Contains(got, "-line 20") || !strings.Contains(got, "+line 20 changed") {
		t.Fatalf("compact diff should include changed lines:\n%s", got)
	}
}

func TestUnifiedDiffFullPrintsWholeFile(t *testing.T) {
	got := unifiedDiff("one\ntwo\nthree\n", "one\nTWO\nthree\n", true)
	if !strings.Contains(got, "@@ -1,3 +1,3 @@") {
		t.Fatalf("full diff should cover the whole file:\n%s", got)
	}
	if !strings.Contains(got, " one\n") || !strings.Contains(got, " three\n") {
		t.Fatalf("full diff should include unchanged lines:\n%s", got)
	}
}
