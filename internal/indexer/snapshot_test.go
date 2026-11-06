package indexer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestCatalogSnapshotResolutionAndPrecedence(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.employees (id NUMBER);
CREATE PROCEDURE hr.p IS BEGIN
 SELECT salary FROM employees;
 SELECT id FROM staff;
 sys.dbms_output.put_line('hello');
END;
/
`
	catalog := `{"objects":[
 {"schema":"HR","name":"EMPLOYEES","kind":"table","columns":[{"name":"ID","type":"NUMBER"},{"name":"SALARY","type":"NUMBER"}]},
 {"schema":"PUBLIC","name":"STAFF","kind":"synonym","target":{"schema":"HR","name":"EMPLOYEES"}},
 {"schema":"SYS","name":"DBMS_OUTPUT","kind":"package","members":[{"name":"PUT_LINE","kind":"procedure","parameters":[{"name":"A","type":"VARCHAR2"}]}]}
 ]}`
	for file, data := range map[string]string{"a.sql": sql, "dictionary.json": catalog} {
		if err := os.WriteFile(filepath.Join(root, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	opts := Options{Root: root, Output: "index.scip", Catalog: "dictionary.json", Database: "demo", Version: "1", Schema: "HR"}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, again) {
		t.Fatal("snapshot output is nondeterministic")
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	occ := map[[2]int32]string{}
	for _, o := range idx.Documents[0].Occurrences {
		if len(o.Diagnostics) > 0 {
			t.Errorf("unexpected diagnostic: %v", o.Diagnostics)
		}
		if o.Symbol != "" {
			occ[[2]int32{o.Range[0], o.Range[1]}] = o.Symbol
		}
	}
	checks := map[[2]int32]string{{2, 8}: "HR/EMPLOYEES#SALARY.", {3, 8}: "HR/EMPLOYEES#ID.", {3, 16}: "HR/EMPLOYEES#", {4, 5}: "SYS/DBMS_OUTPUT#", {4, 17}: "SYS/DBMS_OUTPUT#PUT_LINE()."}
	for pos, want := range checks {
		want = "scip-plsql oracle demo 1 " + want
		if occ[pos] != want {
			t.Errorf("%v got %q want %q; all %v", pos, occ[pos], want, occ)
		}
	}
	external := map[string]*scip.SymbolInformation{}
	for _, sym := range idx.ExternalSymbols {
		external[sym.Symbol] = sym
	}
	for _, want := range []string{"HR/EMPLOYEES#SALARY.", "PUBLIC/STAFF#", "SYS/DBMS_OUTPUT#", "SYS/DBMS_OUTPUT#PUT_LINE().", "SYS/DBMS_OUTPUT#PUT_LINE().(A)"} {
		if external["scip-plsql oracle demo 1 "+want] == nil {
			t.Errorf("missing external %s", want)
		}
	}
	for _, defined := range []string{"HR/EMPLOYEES#", "HR/EMPLOYEES#ID."} {
		if external["scip-plsql oracle demo 1 "+defined] != nil {
			t.Errorf("repo definition emitted as external: %s", defined)
		}
	}
	alias := external["scip-plsql oracle demo 1 PUBLIC/STAFF#"]
	if alias == nil || len(alias.Relationships) != 1 || !alias.Relationships[0].IsReference || alias.Relationships[0].Symbol != "scip-plsql oracle demo 1 HR/EMPLOYEES#" {
		t.Errorf("synonym relation: %v", alias)
	}
}

func TestCatalogSnapshotRejectsInvalidOrDuplicateObjects(t *testing.T) {
	for _, input := range []string{`{"objects":[{"schema":"HR","name":"T","kind":"table"},{"schema":"HR","name":"T","kind":"view"}]}`, `{"objects":[{"schema":"HR","name":"T","kind":"typo"}]}`, `{"objects":[]} {}`, `{"objects":[],"unknown":true}`} {
		root := t.TempDir()
		path := filepath.Join(root, "catalog.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readSnapshot(path); err == nil {
			t.Errorf("accepted invalid snapshot %s", input)
		}
	}
}

func TestCatalogSnapshotOverloadsAndQuotedTable(t *testing.T) {
	root := t.TempDir()
	sql := `SELECT "Wage" FROM hr."Emp";
CREATE PROCEDURE hr.p IS BEGIN sys.pkg.process('text'); END;
/
`
	snapshot := `{"objects":[{"schema":"HR","name":"Emp","kind":"table","columns":[{"name":"Wage"}]},{"schema":"SYS","name":"PKG","kind":"package","members":[{"name":"PROCESS","kind":"procedure","parameters":[{"name":"N","type":"NUMBER"}]},{"name":"PROCESS","kind":"procedure","parameters":[{"name":"S","type":"VARCHAR2"}]}]}]}`
	for file, data := range map[string]string{"a.sql": sql, "catalog.json": snapshot} {
		if err := os.WriteFile(filepath.Join(root, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Index(Options{Root: root, Output: "index.scip", Catalog: "catalog.json", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol != "" {
			found[o.Symbol] = true
		}
	}
	for _, symbol := range []string{"scip-plsql oracle demo 1 HR/Emp#Wage.", "scip-plsql oracle demo 1 HR/Emp#", "scip-plsql oracle demo 1 SYS/PKG#PROCESS(+1)."} {
		if !found[symbol] {
			t.Errorf("missing %s: %v", symbol, found)
		}
	}
}

func TestSnapshotDoesNotAttachColumnsToConflictingRepositoryObject(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE PACKAGE hr.same_name AS PROCEDURE run; END;\n/\n"
	catalog := `{"objects":[{"schema":"HR","name":"SAME_NAME","kind":"table","columns":[{"name":"COL"}]}]}`
	for file, data := range map[string]string{"a.sql": sql, "catalog.json": catalog} {
		if err := os.WriteFile(filepath.Join(root, file), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Index(Options{Root: root, Output: "index.scip", Catalog: "catalog.json", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	for _, sym := range idx.ExternalSymbols {
		if sym.Symbol == "scip-plsql oracle demo 1 HR/SAME_NAME#COL." {
			t.Fatal("snapshot table column attached to repository package")
		}
	}
}
