package indexer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bytebase/omni/oracle/ast"
	"github.com/scip-code/scip/bindings/go/scip"
)

func TestDeclarationSignatureAndDocumentation(t *testing.T) {
	text := "-- Paquete de ejemplo\nCREATE OR REPLACE PACKAGE HR.P AS\n  PROCEDURE X;\nEND P;\n"
	start := strings.Index(text, "-- Paquete")
	headerStart, _, signature := declarationSignature(text, start, len(text))
	if signature != "CREATE OR REPLACE PACKAGE HR.P" {
		t.Fatalf("signature = %q", signature)
	}
	if got := leadingDocumentation(text, headerStart); got != "Paquete de ejemplo" {
		t.Fatalf("documentation = %q", got)
	}
}

func TestAddAppliesSCIPDeclarationMetadata(t *testing.T) {
	text := "-- función pública\nCREATE FUNCTION HR.F RETURN NUMBER AS BEGIN RETURN 1; END;\n"
	lineStarts := []int{0}
	for i, r := range text {
		if r == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	start := strings.Index(text, "CREATE")
	end := len(text)
	src := &source{text: text, lineStarts: lineStarts, doc: &scip.Document{}}
	setSymbolMetadata(src, "sym", symbolMetadata{
		enclosingSymbol: "parent#", enclosingStart: start, enclosingEnd: end,
		documentation: "función pública", signature: "CREATE FUNCTION HR.F RETURN NUMBER",
	})
	add(src, decl{symbol: "sym", name: "F", start: start, end: start + len("CREATE"), role: int32(scip.SymbolRole_Definition), kind: scip.SymbolInformation_Function})

	if len(src.doc.Occurrences) != 1 || !reflect.DeepEqual(src.doc.Occurrences[0].EnclosingRange, src.rng(start, end)) {
		t.Fatalf("occurrence enclosing range = %#v", src.doc.Occurrences)
	}
	if len(src.doc.Symbols) != 1 {
		t.Fatalf("got %d symbol infos", len(src.doc.Symbols))
	}
	info := src.doc.Symbols[0]
	if info.EnclosingSymbol != "parent#" || info.SignatureDocumentation == nil || info.SignatureDocumentation.Text != "CREATE FUNCTION HR.F RETURN NUMBER" || !reflect.DeepEqual(info.Documentation, []string{"función pública"}) {
		t.Fatalf("symbol metadata = %#v", info)
	}
}

func TestDeclareUnitAttachesSignatureDocumentationAndEnclosingRanges(t *testing.T) {
	text := "-- paquete HR.P\nCREATE OR REPLACE PACKAGE HR.P AS\n  -- método X\n  PROCEDURE X;\nEND P;\n"
	list, err := parseRange(text, 0, len(text))
	if err != nil {
		t.Fatal(err)
	}
	var node ast.Node
	for _, item := range list.Items {
		if raw, ok := item.(*ast.RawStmt); ok {
			node = raw.Stmt
			break
		}
	}
	if node == nil {
		t.Fatal("parser returned no PL/SQL object")
	}
	lineStarts := []int{0}
	for i, r := range text {
		if r == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	src := &source{text: text, processed: text, path: "package.sql", lineStarts: lineStarts, doc: &scip.Document{}, symbolMetadata: map[string]symbolMetadata{}}
	state := &state{
		opts: Options{Schema: "HR"}, objects: map[string]string{}, objectKinds: map[string]scip.SymbolInformation_Kind{},
		synonyms: map[string]synonym{}, methods: map[string][]*method{}, standalone: map[string]*method{},
		methodNodes: map[ast.Node]*method{}, external: map[string]*scip.SymbolInformation{},
		packageBindings: map[string]map[string]string{}, packageRowTypes: map[string]map[string]string{},
		typeBindings: map[string]map[string]string{}, typeParents: map[string]string{},
	}
	state.declareUnit(src, unit{start: 0, end: len(text), schema: "HR", node: node})

	var pkg, method *scip.SymbolInformation
	var pkgOccurrence *scip.Occurrence
	for _, info := range src.doc.Symbols {
		switch info.DisplayName {
		case "P":
			pkg = info
		case "X":
			method = info
		}
	}
	for _, occurrence := range src.doc.Occurrences {
		if strings.HasSuffix(occurrence.Symbol, "HR/P#") {
			pkgOccurrence = occurrence
		}
	}
	if pkg == nil || method == nil || pkgOccurrence == nil {
		t.Fatalf("missing package/method definitions: symbols=%+v occurrences=%+v", src.doc.Symbols, src.doc.Occurrences)
	}
	if pkg.SignatureDocumentation == nil || !strings.Contains(pkg.SignatureDocumentation.Text, "CREATE OR REPLACE PACKAGE HR.P") || !reflect.DeepEqual(pkg.Documentation, []string{"paquete HR.P"}) {
		t.Fatalf("package documentation = %#v", pkg)
	}
	if !strings.HasSuffix(method.EnclosingSymbol, "HR/P#") || !reflect.DeepEqual(method.Documentation, []string{"método X"}) {
		t.Fatalf("method documentation = %#v", method)
	}
	if !reflect.DeepEqual(pkgOccurrence.EnclosingRange, src.rng(0, len(text))) {
		t.Fatalf("package enclosing range = %v", pkgOccurrence.EnclosingRange)
	}
}
