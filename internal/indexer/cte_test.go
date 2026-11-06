package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestCTEColumnsAndSourceResolution(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
WITH c (person_id, wage) AS (SELECT id, salary FROM emp)
SELECT c.person_id, c.wage FROM c;
`
	r, d := scip.SymbolRole_ReadAccess, scip.SymbolRole_Definition
	checkDML(t, sql, []expectedDML{
		{1, "person_id", 0, "local 1", d}, {1, "wage", 0, "local 2", d}, {1, "id", 1, "HR/EMP#ID.", r}, {1, "salary", 0, "HR/EMP#SALARY.", r},
		{2, "person_id", 0, "local 1", r}, {2, "wage", 0, "local 2", r},
	})
}
func TestDerivedProjectionAliasesAndDirectColumns(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
SELECT s.emp_id, s.total FROM (SELECT e.id AS emp_id, e.salary * 2 AS total FROM emp e) s;
`
	r, d := scip.SymbolRole_ReadAccess, scip.SymbolRole_Definition
	checkDML(t, sql, []expectedDML{
		{1, "emp_id", 0, "local 0", r}, {1, "total", 0, "local 1", r}, {1, "id", 1, "HR/EMP#ID.", r}, {1, "emp_id", 1, "local 0", d}, {1, "salary", 0, "HR/EMP#SALARY.", r}, {1, "total", 1, "local 1", d},
	})
}

func TestDerivedAliasUsesItsOwnTokenWhenRepeatedInExpression(t *testing.T) {
	sql := `CREATE TABLE hr.t (total NUMBER);
SELECT d.total FROM (SELECT total + total AS total FROM t) d;
`
	r, d := scip.SymbolRole_ReadAccess, scip.SymbolRole_Definition
	checkDML(t, sql, []expectedDML{
		{1, "total", 0, "local 0", r},
		{1, "total", 1, "HR/T#TOTAL.", r},
		{1, "total", 2, "HR/T#TOTAL.", r},
		{1, "total", 3, "local 0", d},
	})
}

func TestSelectAliasResolvesInOrderBy(t *testing.T) {
	sql := `CREATE TABLE hr.emp (salary NUMBER);
SELECT salary AS wage FROM emp ORDER BY wage;
`
	r, d := scip.SymbolRole_ReadAccess, scip.SymbolRole_Definition
	checkDML(t, sql, []expectedDML{
		{1, "salary", 0, "HR/EMP#SALARY.", r}, {1, "wage", 0, "local 0", d}, {1, "wage", 1, "local 0", r},
	})
}
func TestUnknownProjectionDoesNotCapturePLSQLVariable(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PROCEDURE hr.p(x NUMBER) IS y NUMBER; BEGIN
 WITH c AS (SELECT * FROM unknown_table) SELECT x INTO y FROM c;
END;
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
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 1 && o.Symbol == "scip-plsql oracle demo 1 HR/P().(X)" {
			t.Errorf("unknown CTE could capture x: %v", o)
		}
	}
}
