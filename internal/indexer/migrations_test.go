package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestFlywayMigrationsReproduceFinalColumns(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"V1__create.sql":  "CREATE TABLE hr.emp (id NUMBER, obsolete NUMBER);\n",
		"V2__add.sql":     "ALTER TABLE hr.emp ADD (salary NUMBER, \"Other\" NUMBER);\n",
		"V10__rename.sql": "ALTER TABLE hr.emp RENAME COLUMN salary TO pay;\nALTER TABLE hr.emp DROP (obsolete, \"Other\");\n",
		"queries.sql":     "SELECT id, pay, salary, obsolete, \"Other\" FROM hr.emp;\n",
	}
	for p, data := range files {
		if err := os.WriteFile(filepath.Join(root, p), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
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
	var query, rename, addition *scip.Document
	for _, doc := range idx.Documents {
		switch doc.RelativePath {
		case "queries.sql":
			query = doc
		case "V10__rename.sql":
			rename = doc
		case "V2__add.sql":
			addition = doc
		}
	}
	if query == nil || rename == nil || addition == nil {
		t.Fatal("missing migration documents")
	}
	refs := map[string]bool{}
	for _, o := range query.Occurrences {
		if o.Symbol != "" {
			refs[o.Symbol] = true
		}
	}
	for _, col := range []string{"ID", "PAY"} {
		if !refs["scip-plsql oracle demo 1 HR/EMP#"+col+"."] {
			t.Errorf("missing final column %s: %v", col, refs)
		}
	}
	for _, col := range []string{"SALARY", "OBSOLETE", "Other"} {
		if refs["scip-plsql oracle demo 1 HR/EMP#"+col+"."] {
			t.Errorf("dropped/renamed column resolved %s", col)
		}
	}
	foundAdd, foundRename := false, false
	for _, o := range addition.Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#SALARY." && o.SymbolRoles == int32(scip.SymbolRole_Definition) {
			foundAdd = true
		}
	}
	for _, o := range rename.Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#PAY." && o.SymbolRoles == int32(scip.SymbolRole_Definition) {
			foundRename = true
		}
	}
	if !foundAdd || !foundRename {
		t.Fatalf("missing migration definitions add=%v rename=%v", foundAdd, foundRename)
	}
}

func TestMigrationsOverlaySnapshotAndRemoveExternalDuplicates(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"dictionary.json": `{"objects":[{"schema":"HR","name":"EMP","kind":"table","columns":[{"name":"ID"},{"name":"OLD_COL"},{"name":"PAY"}]}]}`,
		"V1__add.sql":     "ALTER TABLE hr.emp ADD salary NUMBER;\n",
		"V2__changes.sql": "ALTER TABLE hr.emp RENAME COLUMN salary TO pay;\nALTER TABLE hr.emp DROP COLUMN old_col;\n",
		"query.sql":       "SELECT id, salary, pay, old_col FROM hr.emp;\n",
	}
	for p, data := range files {
		if err := os.WriteFile(filepath.Join(root, p), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Index(Options{Root: root, Output: "index.scip", Catalog: "dictionary.json", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	ext := map[string]bool{}
	for _, v := range idx.ExternalSymbols {
		ext[v.Symbol] = true
	}
	if !ext["scip-plsql oracle demo 1 HR/EMP#ID."] || ext["scip-plsql oracle demo 1 HR/EMP#OLD_COL."] || ext["scip-plsql oracle demo 1 HR/EMP#PAY."] {
		t.Errorf("external symbols after DDL: %v", ext)
	}
	var query *scip.Document
	for _, doc := range idx.Documents {
		if doc.RelativePath == "query.sql" {
			query = doc
		}
	}
	if query == nil {
		t.Fatal("missing query")
	}
	got := map[string]bool{}
	for _, o := range query.Occurrences {
		got[o.Symbol] = true
	}
	if !got["scip-plsql oracle demo 1 HR/EMP#ID."] || !got["scip-plsql oracle demo 1 HR/EMP#PAY."] || got["scip-plsql oracle demo 1 HR/EMP#OLD_COL."] || got["scip-plsql oracle demo 1 HR/EMP#SALARY."] {
		t.Errorf("query refs after DDL: %v", got)
	}
}

func TestFlywayVersionComparison(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{{"V1_2__a.sql", "V1_10__b.sql", -1}, {"V1.10__a.sql", "V1_2__b.sql", 1}, {"V0001_0__a.sql", "V1__b.sql", 0}, {"V999999999999999999999999__a.sql", "V12__b.sql", 1}} {
		a, b := migrationVersion(tc.a), migrationVersion(tc.b)
		if a == nil || b == nil || compareVersion(a, b) != tc.want {
			t.Errorf("%s vs %s = %d", tc.a, tc.b, compareVersion(a, b))
		}
	}
}
