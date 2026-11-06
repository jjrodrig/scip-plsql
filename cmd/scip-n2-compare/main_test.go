package main

import (
	"testing"

	"github.com/jjrodrig/plsql-scip/internal/indexer"
	"github.com/scip-code/scip/bindings/go/scip"
)

func TestCompareAcrossDocumentsAndRoles(t *testing.T) {
	decl := input{entry: entry{Document: "spec", ObjectType: "PACKAGE"}, source: "proc\n", doc: &scip.Document{Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 4}, Symbol: "pkg.proc", SymbolRoles: int32(scip.SymbolRole_ForwardDefinition)}}}, rows: []indexer.PLScopeIdentifier{{Name: "PROC", Signature: "s", Usage: "DECLARATION", ObjectType: "PACKAGE", Line: 1, Column: 1}}}
	call := input{entry: entry{Document: "body", ObjectType: "PACKAGE BODY"}, source: "proc\ncol\n", doc: &scip.Document{Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 4}, Symbol: "pkg.proc", SymbolRoles: int32(scip.SymbolRole_ReadAccess)}, {Range: []int32{1, 0, 3}, Symbol: "table.col", SymbolRoles: int32(scip.SymbolRole_ReadAccess)}}}, rows: []indexer.PLScopeIdentifier{{Name: "PROC", Signature: "s", Usage: "CALL", ObjectType: "PACKAGE BODY", Line: 1, Column: 1}, {Name: "COL", Type: "COLUMN", Signature: "c", Usage: "ASSIGNMENT", ObjectType: "PACKAGE BODY", Line: 2, Column: 1}}}
	r := compare([]input{decl, call}, []binding{{Name: "COL", Type: "COLUMN", Symbol: "table.col"}}, nil)
	if r.Counts["matched"] != 3 || r.Roles["matched"] != 2 || r.Roles["mismatch"] != 1 {
		t.Fatalf("unexpected: %+v", r)
	}
}

func TestColumnReferenceRoleNeedsExplicitFixtureExpectation(t *testing.T) {
	in := input{entry: entry{Document: "body", ObjectType: "PROCEDURE"}, source: "col\n", doc: &scip.Document{Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 3}, Symbol: "t.col", SymbolRoles: int32(scip.SymbolRole_WriteAccess)}}}, rows: []indexer.PLScopeIdentifier{{Name: "COL", Type: "COLUMN", Signature: "c", Usage: "REFERENCE", ObjectType: "PROCEDURE", Line: 1, Column: 1}}}
	b := []binding{{Name: "COL", Type: "COLUMN", Symbol: "t.col"}}
	if got := compare([]input{in}, b, nil); got.Roles["not_checked"] != 1 {
		t.Fatalf("column reference does not imply read: %+v", got)
	}
	roles := []roleExpectation{{Document: "body", Name: "COL", Line: 1, Column: 1, Role: "write"}}
	if got := compare([]input{in}, b, roles); got.Roles["matched"] != 1 || got.Items[0].RoleSource != "fixture" {
		t.Fatalf("fixture role not checked: %+v", got)
	}
}

func TestExplicitAnchorDetectsIncorrectDeclaration(t *testing.T) {
	in := input{entry: entry{Document: "spec", ObjectType: "PACKAGE"}, source: "proc\n", doc: &scip.Document{Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 4}, Symbol: "wrong", SymbolRoles: int32(scip.SymbolRole_ForwardDefinition)}}}, rows: []indexer.PLScopeIdentifier{{Name: "PROC", Type: "PROCEDURE", Signature: "s", Usage: "DECLARATION", ObjectType: "PACKAGE", Line: 1, Column: 1}}}
	bindings := []binding{{Name: "PROC", Type: "PROCEDURE", Document: "spec", Line: 1, Column: 1, Symbol: "expected"}}
	if got := compare([]input{in}, bindings, nil); got.Counts["anchor_conflict"] != 1 {
		t.Fatalf("unexpected report: %+v", got)
	}
}

func TestCompareReportsAmbiguousCandidates(t *testing.T) {
	in := input{entry: entry{Document: "call", ObjectType: "PROCEDURE"}, source: "proc\n", doc: &scip.Document{Occurrences: []*scip.Occurrence{{Range: []int32{0, 0, 4}, Symbol: "expected", SymbolRoles: int32(scip.SymbolRole_ReadAccess)}, {Range: []int32{0, 0, 4}, Symbol: "other", SymbolRoles: int32(scip.SymbolRole_ReadAccess)}}}, rows: []indexer.PLScopeIdentifier{{Name: "PROC", Type: "PROCEDURE", Signature: "s", Usage: "CALL", ObjectType: "PROCEDURE", Line: 1, Column: 1}}}
	r := compare([]input{in}, []binding{{Name: "PROC", Type: "PROCEDURE", Symbol: "expected"}}, nil)
	if r.Counts["ambiguous"] != 1 || r.Counts["matched"] != 0 {
		t.Fatalf("unexpected report: %+v", r)
	}
}
