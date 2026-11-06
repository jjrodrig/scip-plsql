package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestRowtypeAndTypeAnchors(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
CREATE PROCEDURE hr.p IS
 r hr.emp%ROWTYPE;
 v hr.emp.salary%TYPE;
BEGIN
 r.salary := v;
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{
		{2, "emp", 0, "HR/EMP#", r}, {3, "emp", 0, "HR/EMP#", r}, {3, "salary", 0, "HR/EMP#SALARY.", r},
		{5, "r", 0, "local 0", r}, {5, "salary", 0, "HR/EMP#SALARY.", w}, {5, "v", 0, "local 1", r},
	})
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
	relationships := map[string]string{}
	for _, info := range idx.Documents[0].Symbols {
		for _, r := range info.Relationships {
			if r.IsTypeDefinition {
				relationships[info.Symbol] = r.Symbol
			}
		}
	}
	if relationships["local 0"] != "scip-plsql oracle demo 1 HR/EMP#" || relationships["local 1"] != "scip-plsql oracle demo 1 HR/EMP#SALARY." {
		t.Errorf("anchor relationships: %v", relationships)
	}
}
func TestTriggerNewOldColumnRoles(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
CREATE TRIGGER hr.trg BEFORE UPDATE ON hr.emp FOR EACH ROW
BEGIN
 :new.salary := :old.salary;
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{{1, "emp", 0, "HR/EMP#", r}, {3, "salary", 0, "HR/EMP#SALARY.", w}, {3, "salary", 1, "HR/EMP#SALARY.", r}})
}
func TestRowtypeShadowingDoesNotGuessField(t *testing.T) {
	sql := `CREATE TABLE hr.emp (id NUMBER);
CREATE PROCEDURE hr.p IS r emp%ROWTYPE; BEGIN
 DECLARE r NUMBER; BEGIN r.id := 1; END;
 r.id := 2;
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
	valid := false
	for _, o := range idx.Documents[0].Occurrences {
		if strings.HasSuffix(o.Symbol, "HR/EMP#ID.") && o.Range[0] == 2 {
			t.Errorf("shadowed row type resolved: %v", o)
		}
		if strings.HasSuffix(o.Symbol, "HR/EMP#ID.") && o.Range[0] == 3 {
			valid = true
		}
	}
	if !valid {
		t.Error("outer row field not resolved")
	}
}

func TestQuotedRowtypeAndCustomTriggerAliases(t *testing.T) {
	sql := `CREATE TABLE hr."Emp" ("Salary" NUMBER);
CREATE PROCEDURE hr.p IS r hr."Emp"%ROWTYPE; BEGIN r."Salary" := 1; END;
/
CREATE TRIGGER hr.trg BEFORE UPDATE ON hr."Emp" REFERENCING NEW AS n OLD AS o FOR EACH ROW
BEGIN :n."Salary" := :o."Salary"; END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{
		{1, `"Emp"`, 0, "HR/Emp#", r}, {1, `"Salary"`, 0, "HR/Emp#Salary.", w},
		{3, `"Emp"`, 0, "HR/Emp#", r}, {4, `"Salary"`, 0, "HR/Emp#Salary.", w}, {4, `"Salary"`, 1, "HR/Emp#Salary.", r},
	})
}

func TestTriggerUsesObjectOwnerForUnqualifiedTable(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.emp (salary NUMBER);
CREATE TRIGGER hr.trg BEFORE UPDATE ON emp FOR EACH ROW BEGIN :new.salary := :old.salary; END;
/
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
	table, read, write := false, false, false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] != 1 {
			continue
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#" {
			table = true
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#SALARY." && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
			read = true
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#SALARY." && o.SymbolRoles == int32(scip.SymbolRole_WriteAccess) {
			write = true
		}
	}
	if !table || !read || !write {
		t.Fatalf("table=%v read=%v write=%v", table, read, write)
	}
}

func TestParameterRowtypeAndPackageAnchors(t *testing.T) {
	sql := `CREATE TABLE hr.emp (salary NUMBER);
CREATE PACKAGE hr.pkg AS
 rate hr.emp.salary%TYPE;
 PROCEDURE adjust(r IN OUT hr.emp%ROWTYPE);
END;
/
CREATE PACKAGE BODY hr.pkg AS
 PROCEDURE adjust(r IN OUT hr.emp%ROWTYPE) IS BEGIN r.salary := rate; END;
END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{
		{2, "emp", 0, "HR/EMP#", r}, {2, "salary", 0, "HR/EMP#SALARY.", r}, {3, "emp", 0, "HR/EMP#", r},
		{7, "emp", 0, "HR/EMP#", r}, {7, "r", 2, "HR/PKG#ADJUST().(R)", r}, {7, "salary", 0, "HR/EMP#SALARY.", w}, {7, "rate", 0, "HR/PKG#RATE.", r},
	})
}

func TestPackageRowtypeFromLaterSpec(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a_body.sql": "CREATE PACKAGE BODY hr.pkg AS PROCEDURE run IS BEGIN g_emp.salary := 1; END; END;\n/\n",
		"z_spec.sql": "CREATE PACKAGE hr.pkg AS g_emp hr.emp%ROWTYPE; PROCEDURE run; END;\n/\nCREATE TABLE hr.emp (salary NUMBER);\n",
	}
	for path, sql := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(sql), 0600); err != nil {
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
	row, col := false, false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1 HR/PKG#G_EMP." && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
			row = true
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#SALARY." && o.SymbolRoles == int32(scip.SymbolRole_WriteAccess) {
			col = true
		}
	}
	if !row || !col {
		t.Fatalf("row=%v col=%v; %v", row, col, idx.Documents[0].Occurrences)
	}
}
