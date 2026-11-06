package indexer

import (
	"testing"

	"github.com/sourcegraph/scip/bindings/go/scip"
)

func TestSnapshotFunctionMetadataIncludesReturnTypesAndParents(t *testing.T) {
	state := &state{
		opts:    Options{Database: "demo", Version: "1"},
		objects: map[string]string{}, objectKinds: map[string]scip.SymbolInformation_Kind{},
		synonyms: map[string]synonym{}, methods: map[string][]*method{}, standalone: map[string]*method{},
		external: map[string]*scip.SymbolInformation{}, packageBindings: map[string]map[string]string{},
		packageRowTypes: map[string]map[string]string{}, typeBindings: map[string]map[string]string{}, typeParents: map[string]string{},
	}
	state.mergeSnapshot(CatalogSnapshot{Objects: []CatalogObject{
		{Schema: "HR", Name: "API", Kind: "package", Members: []CatalogMember{{
			Name: "LOOKUP", Kind: "function", ReturnType: "VARCHAR2",
			Parameters: []CatalogParameter{{Name: "P_ID", Type: "NUMBER", Mode: "IN", Default: true}},
		}}},
		{Schema: "HR", Name: "GET_ID", Kind: "function", ReturnType: "NUMBER", Parameters: []CatalogParameter{{Name: "P_CODE", Type: "VARCHAR2"}}},
	}})

	packageSymbol := state.global("HR", "API", "#")
	methodSymbol := packageSymbol + "LOOKUP()."
	methodInfo := state.external[methodSymbol]
	if methodInfo == nil || methodInfo.EnclosingSymbol != packageSymbol || methodInfo.SignatureDocumentation == nil {
		t.Fatalf("external method metadata = %#v", methodInfo)
	}
	if got, want := methodInfo.SignatureDocumentation.Text, "FUNCTION LOOKUP(P_ID IN NUMBER DEFAULT) RETURN VARCHAR2"; got != want {
		t.Fatalf("method signature = %q, want %q", got, want)
	}
	parameterInfo := state.external[methodSymbol+"(P_ID)"]
	if parameterInfo == nil || parameterInfo.EnclosingSymbol != methodSymbol {
		t.Fatalf("external parameter parent = %#v", parameterInfo)
	}
	if got := state.methods["HR.API.LOOKUP"][0].returnType; got != "VARCHAR2" {
		t.Fatalf("method return type = %q", got)
	}

	standaloneSymbol := state.global("HR", "GET_ID", "().")
	standaloneInfo := state.external[standaloneSymbol]
	if standaloneInfo == nil || standaloneInfo.SignatureDocumentation == nil || standaloneInfo.SignatureDocumentation.Text != "FUNCTION GET_ID(P_CODE IN VARCHAR2) RETURN NUMBER" {
		t.Fatalf("standalone function metadata = %#v", standaloneInfo)
	}
	if got := state.standalone["HR.GET_ID"].returnType; got != "NUMBER" {
		t.Fatalf("standalone return type = %q", got)
	}
}
