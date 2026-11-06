package main

import (
	"testing"

	"github.com/jjrodrig/plsql-scip/internal/indexer"
	"github.com/scip-code/scip/bindings/go/scip"
)

func TestCompareUsesSignatureAndByteOffsets(t *testing.T) {
	source := "create function foo\n  é := foo;\nfoo\n"
	doc := &scip.Document{Occurrences: []*scip.Occurrence{
		{Range: []int32{0, 16, 19}, Symbol: "symbol-foo"},
		{Range: []int32{1, 8, 11}, Symbol: "symbol-foo"},
		{Range: []int32{2, 0, 3}, Symbol: "different-symbol"},
	}}
	rows := []indexer.PLScopeIdentifier{
		{Name: "FOO", Signature: "1", Usage: "DEFINITION", Line: 1, Column: 10},
		{Name: "FOO", Signature: "1", Usage: "REFERENCE", Line: 2, Column: 8},
		{Name: "FOO", Signature: "1", Usage: "REFERENCE", Line: 3, Column: 1},
		{Name: "FOO", Signature: "external", Usage: "CALL", Line: 3, Column: 1},
		{Name: "OTHER", Signature: "2", Usage: "REFERENCE", Line: 2, Column: 8},
	}
	r := compare(doc, source, rows, 7)
	if r.Counts["matched"] != 2 || r.Counts["source_mismatch"] != 1 || r.Counts["different_symbol"] != 1 || r.Counts["unanchored"] != 1 {
		t.Fatalf("unexpected report: %+v", r)
	}
}
