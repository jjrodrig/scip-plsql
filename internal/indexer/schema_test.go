package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestSessionSchemaOrderAndFileIsolation(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a.sql": `CREATE TABLE before_change (id NUMBER);
ALTER SESSION SET CURRENT_SCHEMA = hr;
CREATE TABLE employees (id NUMBER);
CREATE PACKAGE pkg AS PROCEDURE run; END;
/
CREATE PROCEDURE p IS BEGIN pkg.run(); END;
/
ALTER SESSION SET CURRENT_SCHEMA = sales;
CREATE TABLE orders (id NUMBER);
SELECT id FROM orders;
CREATE TABLE hr.explicit_table (id NUMBER);
`,
		"b.sql": `CREATE TABLE independent_table (id NUMBER);
`,
	}
	for file, data := range files {
		if err := os.WriteFile(filepath.Join(root, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP"}); err != nil {
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
	defs := map[string]bool{}
	refs := map[string]bool{}
	for _, doc := range idx.Documents {
		for _, o := range doc.Occurrences {
			if o.SymbolRoles == int32(scip.SymbolRole_Definition) || o.SymbolRoles == int32(scip.SymbolRole_ForwardDefinition) {
				defs[o.Symbol] = true
			}
			if o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
				refs[o.Symbol] = true
			}
			for _, d := range o.Diagnostics {
				t.Errorf("unexpected diagnostic: %v", d)
			}
		}
	}
	for _, id := range []string{"APP/BEFORE_CHANGE#", "HR/EMPLOYEES#", "HR/PKG#RUN().", "HR/P().", "SALES/ORDERS#", "HR/EXPLICIT_TABLE#", "APP/INDEPENDENT_TABLE#"} {
		if !defs["scip-plsql oracle demo 1 "+id] {
			t.Errorf("missing definition %s", id)
		}
	}
	for _, id := range []string{"HR/PKG#RUN().", "SALES/ORDERS#", "SALES/ORDERS#ID."} {
		if !refs["scip-plsql oracle demo 1 "+id] {
			t.Errorf("missing reference %s", id)
		}
	}
}
func TestSchemaPathAndSessionPrecedence(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "db", "HR")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	sql := `CREATE TABLE local_table (id NUMBER);
ALTER SESSION SET CURRENT_SCHEMA = sales;
CREATE TABLE session_table (id NUMBER);
CREATE TABLE other.explicit_table (id NUMBER);
`
	if err := os.WriteFile(filepath.Join(dir, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP", SchemaPath: `^db/([^/]+)/`}); err != nil {
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
	defs := map[string]bool{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.SymbolRoles == int32(scip.SymbolRole_Definition) {
			defs[o.Symbol] = true
		}
	}
	for _, id := range []string{"HR/LOCAL_TABLE#", "SALES/SESSION_TABLE#", "OTHER/EXPLICIT_TABLE#"} {
		if !defs["scip-plsql oracle demo 1 "+id] {
			t.Errorf("missing %s: %v", id, defs)
		}
	}
}
func TestInvalidSchemaPathPattern(t *testing.T) {
	for _, pattern := range []string{"[", `^db/[^/]+/`, `(a)(b)`} {
		if _, err := schemaPattern(pattern); err == nil {
			t.Errorf("accepted invalid pattern %q", pattern)
		}
	}
}

func TestQuotedSessionSchemaAndDegradedUnit(t *testing.T) {
	root := t.TempDir()
	sql := `ALTER SESSION SET CURRENT_SCHEMA = "Mixed";
CREATE TABLE some_table (id NUMBER);
SELECT id FROM some_table;
CREATE PROCEDURE big_proc IS BEGIN NULL; NULL; NULL; END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP", MaxTokens: 12}); err != nil {
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
	got := map[string]bool{}
	for _, o := range idx.Documents[0].Occurrences {
		got[o.Symbol] = true
	}
	for _, id := range []string{"Mixed/SOME_TABLE#", "Mixed/SOME_TABLE#ID.", "Mixed/BIG_PROC()."} {
		if !got["scip-plsql oracle demo 1 "+id] {
			t.Errorf("missing %s: %v", id, got)
		}
	}
}

func TestUnknownSessionSchemaDoesNotReusePreviousSchema(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE app.target (id NUMBER);
ALTER SESSION SET CURRENT_SCHEMA = &schema;
CREATE TABLE unknown_owner (id NUMBER);
SELECT id FROM target;
CREATE TABLE hr.explicit_table (id NUMBER);
ALTER SESSION SET CURRENT_SCHEMA = sales;
CREATE TABLE known_owner (id NUMBER);
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP"}); err != nil {
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
	diag, explicit, known := false, false, false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1 APP/UNKNOWN_OWNER#" || (o.Range[0] == 3 && o.Symbol == "scip-plsql oracle demo 1 APP/TARGET#") {
			t.Errorf("incorrectly reused old schema: %v", o)
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EXPLICIT_TABLE#" {
			explicit = true
		}
		if o.Symbol == "scip-plsql oracle demo 1 SALES/KNOWN_OWNER#" {
			known = true
		}
		for _, d := range o.Diagnostics {
			if d.Code == "current-schema" {
				diag = true
			}
		}
	}
	if !diag || !explicit || !known {
		t.Fatalf("unknown schema diagnostic=%v explicit=%v recovered=%v", diag, explicit, known)
	}
}

func TestSessionSchemaSurvivesTokenBudgetDegradation(t *testing.T) {
	root := t.TempDir()
	sql := "ALTER SESSION SET CURRENT_SCHEMA = hr;\nCREATE TABLE employees (id NUMBER);\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP", MaxTokens: 3}); err != nil {
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
	found := false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMPLOYEES#" {
			found = true
		}
	}
	if !found {
		t.Fatalf("schema change was lost: %v", idx.Documents[0].Occurrences)
	}
}

func TestSessionSchemaAppliesToSynonyms(t *testing.T) {
	root := t.TempDir()
	sql := `ALTER SESSION SET CURRENT_SCHEMA = hr;
CREATE TABLE base_table (id NUMBER);
CREATE SYNONYM alias FOR base_table;
SELECT id FROM alias;
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP"}); err != nil {
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
	table, col := false, false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 3 && o.Symbol == "scip-plsql oracle demo 1 HR/BASE_TABLE#" {
			table = true
		}
		if o.Range[0] == 3 && o.Symbol == "scip-plsql oracle demo 1 HR/BASE_TABLE#ID." {
			col = true
		}
	}
	if !table || !col {
		t.Errorf("table=%v col=%v", table, col)
	}
}
