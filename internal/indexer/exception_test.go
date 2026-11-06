package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestRaiseReferencesDeclaredException(t *testing.T) {
	root := t.TempDir()
	src := "CREATE PROCEDURE hr.p IS\n  missing EXCEPTION;\nBEGIN\n  RAISE missing;\nEND;\n/\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	var definition, reference string
	for _, o := range idx.Documents[0].Occurrences {
		if len(o.Range) > 0 && o.Range[0] == 1 {
			definition = o.Symbol
		}
		if len(o.Range) > 0 && o.Range[0] == 3 && o.SymbolRoles&int32(scip.SymbolRole_ReadAccess) != 0 {
			reference = o.Symbol
		}
	}
	if definition == "" || reference != definition {
		t.Fatalf("definition=%q reference=%q", definition, reference)
	}
}
