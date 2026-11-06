package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestOutAndInOutArgumentRoles(t *testing.T) {
	sql := `CREATE PACKAGE hr.pkg AS
 PROCEDURE run(a IN NUMBER, b OUT NUMBER, c IN OUT NOCOPY NUMBER);
END;
/
CREATE PROCEDURE hr.standalone(value OUT NUMBER) IS BEGIN NULL; END;
/
CREATE PROCEDURE hr.client IS x NUMBER; y NUMBER; z NUMBER; BEGIN
 pkg.run(x, b => y, c => z);
 hr.standalone(x);
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{{7, "x", 0, "local 0", r}, {7, "y", 0, "local 1", w}, {7, "z", 0, "local 2", w}, {8, "x", 0, "local 0", w}})
}
func TestOutArgumentRecordFieldAndShadowing(t *testing.T) {
	sql := `CREATE TABLE hr.emp (salary NUMBER);
CREATE PACKAGE hr.pkg AS PROCEDURE set_value(v OUT NUMBER); END;
/
CREATE PROCEDURE hr.client IS r hr.emp%ROWTYPE; BEGIN
 pkg.set_value(r.salary);
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{{4, "r", 0, "local 0", r}, {4, "salary", 0, "HR/EMP#SALARY.", w}})
}
func TestAmbiguousOutModesRemainConservative(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PACKAGE hr.pkg AS PROCEDURE run(p IN NUMBER); PROCEDURE run(p OUT VARCHAR2); END;
/
CREATE PROCEDURE hr.client IS x NUMBER; BEGIN pkg.run(x); END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == "local 0" && o.Range[0] == 2 && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess|scip.SymbolRole_WriteAccess) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected combined role for ambiguous call: %v", idx.Documents[0].Occurrences)
	}
}

func TestDynamicUsingIntoAndFetchRoles(t *testing.T) {
	sql := `CREATE PROCEDURE hr.client IS x NUMBER; y NUMBER; z NUMBER; BEGIN
 EXECUTE IMMEDIATE 'BEGIN p(:1,:2,:3); END;' USING IN x, OUT y, IN OUT z;
 EXECUTE IMMEDIATE 'select col from tab' INTO x;
 EXECUTE IMMEDIATE 'update t set c=1 returning c into :1' RETURNING INTO y;
 FETCH cursor_name INTO z LIMIT x;
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{{1, "x", 0, "local 0", r}, {1, "y", 0, "local 1", w}, {1, "z", 0, "local 2", w}, {2, "x", 0, "local 0", w}, {3, "y", 0, "local 1", w}, {4, "z", 0, "local 2", w}, {4, "x", 0, "local 0", r}})
}

func TestOutCollectionElementReadsIndex(t *testing.T) {
	sql := `CREATE PACKAGE hr.pkg AS PROCEDURE put(v OUT NUMBER); END;
/
CREATE PROCEDURE hr.client IS
 TYPE numbers IS TABLE OF NUMBER INDEX BY BINARY_INTEGER;
 a numbers; i NUMBER;
BEGIN pkg.put(a(i)); END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{{5, "a", 0, "local 0", w}, {5, "i", 0, "local 1", r}})
}
func TestSnapshotParameterModes(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PROCEDURE hr.client IS x NUMBER; y NUMBER; BEGIN
 sys.ext_pkg.put(x);
 sys.standalone(y);
END;
/
`
	snapshot := `{"objects":[{"schema":"SYS","name":"EXT_PKG","kind":"package","members":[{"name":"PUT","kind":"procedure","parameters":[{"name":"VALUE","type":"NUMBER","mode":"OUT"}]}]},{"schema":"SYS","name":"STANDALONE","kind":"procedure","parameters":[{"name":"VALUE","type":"NUMBER","mode":"IN OUT"}]}]}`
	for name, data := range map[string]string{"a.sql": sql, "catalog.json": snapshot} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Catalog: "catalog.json", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	writes := map[string]bool{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] > 0 && o.SymbolRoles == int32(scip.SymbolRole_WriteAccess) {
			writes[o.Symbol] = true
		}
	}
	if !writes["local 0"] || !writes["local 1"] {
		t.Fatalf("external OUT modes: %v", writes)
	}
	found := false
	for _, e := range idx.ExternalSymbols {
		if e.Symbol == "scip-plsql oracle demo 1 SYS/STANDALONE().(VALUE)" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing external standalone parameter")
	}
}
func TestQuotedNamedArgumentsMatchExactly(t *testing.T) {
	sql := `CREATE PACKAGE hr.pkg AS PROCEDURE run("OutArg" OUT NUMBER); END;
/
CREATE PROCEDURE hr.client IS x NUMBER; BEGIN pkg.run("OutArg" => x); END;
/
`
	checkDML(t, sql, []expectedDML{{2, "x", 1, "local 0", scip.SymbolRole_WriteAccess}})
}

func TestUnqualifiedStandaloneCallAndNonCallableObject(t *testing.T) {
	sql := `CREATE PROCEDURE hr.process(value OUT NUMBER) IS BEGIN NULL; END;
/
CREATE TABLE hr.t (id NUMBER);
CREATE PROCEDURE hr.client IS x NUMBER; BEGIN process(x); t(x); END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{{3, "process", 0, "HR/PROCESS().", r}, {3, "x", 1, "local 0", w}})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 3 && o.Symbol == "scip-plsql oracle demo 1 HR/T#" {
			t.Errorf("table misresolved as subprogram: %v", o)
		}
	}
}

func TestLocalObjectShadowsSchemaQualification(t *testing.T) {
	sql := `CREATE TABLE app.hr (id NUMBER);
CREATE PROCEDURE hr.write_x(v OUT NUMBER) IS BEGIN NULL; END;
/
CREATE PROCEDURE app.client IS x NUMBER; BEGIN hr.write_x(x); END;
/
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "APP"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 3 && (o.Symbol == "scip-plsql oracle demo 1 HR/WRITE_X()." || (o.Symbol == "local 0" && o.SymbolRoles&int32(scip.SymbolRole_WriteAccess) != 0)) {
			t.Errorf("shadowed schema resolved as callable: %v", o)
		}
	}
}

func TestParameterModeChangeInvalidatesCachedCaller(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	spec := filepath.Join(root, "pkg.sql")
	client := filepath.Join(root, "client.sql")
	if err := os.WriteFile(spec, []byte("CREATE PACKAGE hr.pkg AS PROCEDURE run(x OUT NUMBER); END;\n/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(client, []byte("CREATE PROCEDURE hr.client IS x NUMBER; BEGIN pkg.run(x); END;\n/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	var stats CacheStats
	opts := Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR", CacheDir: cache, CacheStats: &stats}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	if stats.Hits != 2 {
		t.Fatalf("warm cache: %+v", stats)
	}
	if err := os.WriteFile(spec, []byte("CREATE PACKAGE hr.pkg AS PROCEDURE run(x IN NUMBER); END;\n/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	if stats.Hits != 0 || stats.Misses != 2 {
		t.Fatalf("mode change not invalidated: %+v", stats)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, doc := range idx.Documents {
		if doc.RelativePath != "client.sql" {
			continue
		}
		for _, o := range doc.Occurrences {
			if o.Symbol == "local 0" && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("caller did not receive updated IN role")
	}
}
