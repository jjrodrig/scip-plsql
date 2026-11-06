package main

import (
	"testing"

	"github.com/sourcegraph/scip/bindings/go/scip"
)

func TestSummarizeGroupsAndOrdersDiagnostics(t *testing.T) {
	idx := &scip.Index{Documents: []*scip.Document{{RelativePath: "x.sql", Occurrences: []*scip.Occurrence{
		{Range: []int32{2, 0, 1}, Diagnostics: []*scip.Diagnostic{{Code: "parse-error", Message: "Near USING"}}},
		{Range: []int32{4, 0, 1}, Diagnostics: []*scip.Diagnostic{{Code: "parse-error", Message: "near using"}, {Code: "wrapped", Message: "Wrapped"}}},
	}}}}
	got := summarize(idx)
	if len(got) != 2 || got[0].Count != 2 || got[0].Code != "parse-error" || got[0].Examples[0].Line != 3 || got[1].Count != 1 {
		t.Fatalf("unexpected groups: %+v", got)
	}
}
