package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

type expectedDML struct {
	line   int
	text   string
	nth    int
	symbol string
	role   scip.SymbolRole
}

func checkDML(t *testing.T, sql string, checks []expectedDML) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
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
	got := map[[2]int32]*scip.Occurrence{}
	for _, o := range idx.Documents[0].Occurrences {
		for _, d := range o.Diagnostics {
			t.Errorf("unexpected diagnostic: %v", d)
		}
		if o.Symbol != "" {
			got[[2]int32{o.Range[0], o.Range[1]}] = o
		}
	}
	lines := strings.Split(sql, "\n")
	for _, check := range checks {
		pos, start := -1, 0
		for i := 0; i <= check.nth; i++ {
			p := strings.Index(lines[check.line][start:], check.text)
			if p < 0 {
				t.Fatalf("missing substring %q", check.text)
			}
			pos = start + p
			start = pos + len(check.text)
		}
		o := got[[2]int32{int32(check.line), int32(pos)}]
		expected := check.symbol
		if !strings.HasPrefix(expected, "local ") {
			expected = "scip-plsql oracle demo 1 " + expected
		}
		if o == nil || o.Symbol != expected || o.SymbolRoles != int32(check.role) {
			t.Errorf("%d:%d %q got %v want %s role %v", check.line, pos, check.text, o, expected, check.role)
		}
	}
}
func TestUpdateDeleteAndCorrelatedSubqueries(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
CREATE TABLE hr.dept (id NUMBER);
CREATE PROCEDURE hr.p IS result NUMBER; BEGIN
 UPDATE emp e SET e.salary = e.salary + 1 WHERE e.id IN (SELECT id FROM dept d WHERE d.id = e.id);
 DELETE FROM emp e WHERE e.id = 3 RETURNING e.salary INTO result;
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{
		{3, "salary", 0, "HR/EMP#SALARY.", w}, {3, "salary", 1, "HR/EMP#SALARY.", r}, {3, "id", 0, "HR/EMP#ID.", r}, {3, "id", 1, "HR/DEPT#ID.", r}, {3, "id", 2, "HR/DEPT#ID.", r}, {3, "id", 3, "HR/EMP#ID.", r},
		{4, "id", 0, "HR/EMP#ID.", r}, {4, "salary", 0, "HR/EMP#SALARY.", r}, {4, "result", 0, "local 0", w},
	})
}
func TestMergeSourcesAndWriteRoles(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
CREATE TABLE hr.dept (id NUMBER, budget NUMBER);
MERGE INTO emp e USING dept d ON (e.id = d.id)
 WHEN MATCHED THEN UPDATE SET e.salary = d.budget
 WHEN NOT MATCHED THEN INSERT (id, salary) VALUES (d.id, d.budget);
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{
		{2, "emp", 0, "HR/EMP#", r}, {2, "dept", 0, "HR/DEPT#", r}, {2, "id", 0, "HR/EMP#ID.", r}, {2, "id", 1, "HR/DEPT#ID.", r},
		{3, "salary", 0, "HR/EMP#SALARY.", w}, {3, "budget", 0, "HR/DEPT#BUDGET.", r},
		{4, "id", 0, "HR/EMP#ID.", w}, {4, "salary", 0, "HR/EMP#SALARY.", w}, {4, "id", 1, "HR/DEPT#ID.", r}, {4, "budget", 0, "HR/DEPT#BUDGET.", r},
	})
}

func TestMergeDerivedSourceProjection(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
CREATE TABLE hr.dept (id NUMBER, budget NUMBER);
MERGE INTO emp e USING (SELECT d.id AS key_id, d.budget * 2 AS val FROM dept d) q
 ON (e.id = q.key_id) WHEN MATCHED THEN UPDATE SET e.salary = q.val;
`
	r, w, d := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess, scip.SymbolRole_Definition
	checkDML(t, sql, []expectedDML{
		{2, "dept", 0, "HR/DEPT#", r}, {2, "id", 0, "HR/DEPT#ID.", r}, {2, "key_id", 0, "local 0", d},
		{2, "budget", 0, "HR/DEPT#BUDGET.", r}, {2, "val", 0, "local 1", d},
		{3, "id", 0, "HR/EMP#ID.", r}, {3, "key_id", 0, "local 0", r}, {3, "salary", 0, "HR/EMP#SALARY.", w}, {3, "val", 0, "local 1", r},
	})
}

func TestMergeUnknownSourceAndReturningDoNotMisresolve(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, value NUMBER);
CREATE PROCEDURE hr.p IS result NUMBER; BEGIN
 MERGE INTO emp e USING unknown_source u ON (id = 1) WHEN MATCHED THEN UPDATE SET e.value = u.value;
 DELETE FROM emp e WHERE e.id = 1 RETURNING e.value INTO result;
END;
/
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
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
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 2 && o.Symbol == "scip-plsql oracle demo 1 HR/EMP#ID." {
			t.Errorf("ambiguous merge column attributed to target: %v", o)
		}
		if o.Range[0] == 2 && o.Symbol == "scip-plsql oracle demo 1 HR/EMP#VALUE." && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
			t.Errorf("unknown source attributed to target: %v", o)
		}
	}
}

func TestSQLColumnCaptureAndLocalFallback(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
CREATE PROCEDURE hr.p(salary NUMBER, threshold NUMBER) IS result NUMBER; BEGIN
 SELECT salary INTO result FROM emp WHERE id = threshold;
 UPDATE emp SET salary = salary + threshold WHERE id = threshold;
 INSERT INTO emp (id, salary) VALUES (threshold, salary);
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{
		{2, "salary", 0, "HR/EMP#SALARY.", r}, {2, "result", 0, "local 0", w}, {2, "id", 0, "HR/EMP#ID.", r}, {2, "threshold", 0, "HR/P().(THRESHOLD)", r},
		{3, "salary", 0, "HR/EMP#SALARY.", w}, {3, "salary", 1, "HR/EMP#SALARY.", r}, {3, "threshold", 0, "HR/P().(THRESHOLD)", r}, {3, "id", 0, "HR/EMP#ID.", r}, {3, "threshold", 1, "HR/P().(THRESHOLD)", r},
		{4, "id", 0, "HR/EMP#ID.", w}, {4, "salary", 0, "HR/EMP#SALARY.", w}, {4, "threshold", 0, "HR/P().(THRESHOLD)", r}, {4, "salary", 1, "HR/P().(SALARY)", r},
	})
}
func TestUnknownSQLSourceDoesNotResolveLocal(t *testing.T) {
	sql := `CREATE PROCEDURE hr.p(x NUMBER) IS y NUMBER; BEGIN
 SELECT x INTO y FROM unknown_table;
END;
/
`
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
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
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 1 && o.Range[1] == 8 && o.Symbol == "scip-plsql oracle demo 1 HR/P().(X)" {
			t.Errorf("unknown table could capture x: %v", o)
		}
	}
}

func TestQuotedAliasRetainsCase(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
UPDATE hr.emp "MiX" SET "MiX".salary = 2 WHERE "MiX".id = 1;
SELECT "MiX".id FROM hr.emp "MiX";
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{{1, "salary", 0, "HR/EMP#SALARY.", w}, {1, "id", 0, "HR/EMP#ID.", r}, {2, "id", 0, "HR/EMP#ID.", r}})
}

func TestInsertAndUpdateReturningInto(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
CREATE PROCEDURE hr.p IS result NUMBER; BEGIN
 INSERT INTO emp (id, salary) VALUES (1, 2) RETURNING salary INTO result;
 UPDATE emp SET salary = 3 RETURNING salary INTO result;
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{
		{2, "id", 0, "HR/EMP#ID.", w}, {2, "salary", 0, "HR/EMP#SALARY.", w}, {2, "salary", 1, "HR/EMP#SALARY.", r}, {2, "result", 0, "local 0", w},
		{3, "salary", 0, "HR/EMP#SALARY.", w}, {3, "salary", 1, "HR/EMP#SALARY.", r}, {3, "result", 0, "local 0", w},
	})
}
