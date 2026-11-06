package indexer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestIndexDeterministicAndPositions(t *testing.T) {
	root := t.TempDir()
	sql := "-- ñ\nCREATE OR REPLACE PROCEDURE hr.run_it(p_id NUMBER) IS\nBEGIN NULL; END;\n/\nCREATE TABLE hr.employees (salary NUMBER);\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	o := Options{Root: root, Output: "index.scip", Database: "demo", Version: "1.0", Schema: "HR"}
	if err := Index(o); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(root, o.Output))
	if err != nil {
		t.Fatal(err)
	}
	if err := Index(o); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(root, o.Output))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("index is not deterministic")
	}
	var idx scip.Index
	if err := proto.Unmarshal(first, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Documents) != 1 {
		t.Fatalf("documents: %d", len(idx.Documents))
	}
	d := idx.Documents[0]
	found := map[string]*scip.Occurrence{}
	for _, occ := range d.Occurrences {
		found[occ.Symbol] = occ
	}
	for _, symbol := range []string{"scip-plsql oracle demo 1.0 HR/RUN_IT().", "scip-plsql oracle demo 1.0 HR/RUN_IT().(P_ID)", "scip-plsql oracle demo 1.0 HR/EMPLOYEES#", "scip-plsql oracle demo 1.0 HR/EMPLOYEES#SALARY."} {
		if found[symbol] == nil {
			t.Errorf("missing %s; got %v", symbol, found)
		}
	}
	if x := found["scip-plsql oracle demo 1.0 HR/RUN_IT()."]; x != nil && (x.Range[0] != 1 || x.Range[1] != 31 || x.Range[2] != 37) {
		t.Errorf("procedure range: %v", x.Range)
	}
}

func TestUpdateCapturesColumnAndRoles(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE hr.employees (salary NUMBER);\nCREATE PROCEDURE hr.raise_all(salary NUMBER) IS\nBEGIN\n UPDATE employees SET salary = salary * 1.1;\nEND;\n/\n"
	if err := os.WriteFile(filepath.Join(root, "test.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1.0", Schema: "HR"}); err != nil {
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
	got := map[int32]int32{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1.0 HR/EMPLOYEES#SALARY." && o.Range[0] == 3 {
			got[o.Range[1]] = o.SymbolRoles
		}
	}
	if got[22] != int32(scip.SymbolRole_WriteAccess) || got[31] != int32(scip.SymbolRole_ReadAccess) {
		t.Fatalf("column roles: %v", got)
	}
}

func TestPackageSpecAndBody(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE PACKAGE hr.pkg AS\n PROCEDURE run(p NUMBER);\nEND;\n/\nCREATE PACKAGE BODY hr.pkg AS\n PROCEDURE run(p NUMBER) IS BEGIN NULL; END;\nEND;\n/\n"
	if err := os.WriteFile(filepath.Join(root, "pkg.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1.0", Schema: "HR"}); err != nil {
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
	got := map[string][]int32{}
	for _, o := range idx.Documents[0].Occurrences {
		got[o.Symbol] = append(got[o.Symbol], o.SymbolRoles)
	}
	for _, symbol := range []string{"scip-plsql oracle demo 1.0 HR/PKG#", "scip-plsql oracle demo 1.0 HR/PKG#RUN()."} {
		roles := got[symbol]
		if len(roles) != 2 || roles[0] != int32(scip.SymbolRole_ForwardDefinition) || roles[1] != int32(scip.SymbolRole_Definition) {
			t.Errorf("%s roles %v; all %v", symbol, roles, got)
		}
	}
}

func TestSymbolsParse(t *testing.T) {
	for _, s := range []string{"A", "T#", "ñ", "Mixed Name", "a`b"} {
		symbol := "scip-plsql oracle demo 1 " + escape(s) + "/" + escape(s) + "#"
		if _, err := scip.ParseSymbol(symbol); err != nil {
			t.Errorf("invalid SCIP symbol %q: %v", symbol, err)
		}
	}
}

func TestInvalidUnitReportsDiagnostic(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE broken (x NUMBER, );\nCREATE TABLE valid_table (x NUMBER);\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	diagnostic, valid := false, false
	for _, o := range idx.Documents[0].Occurrences {
		diagnostic = diagnostic || len(o.Diagnostics) > 0
		valid = valid || o.Symbol == "scip-plsql oracle demo 1 HR/VALID_TABLE#"
	}
	if !diagnostic || !valid {
		t.Fatalf("diagnostic=%v valid=%v occurrences=%v", diagnostic, valid, idx.Documents[0].Occurrences)
	}
}

func TestLocalScopesAndSQLCapture(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.t (x NUMBER);
CREATE PROCEDURE hr.p(x NUMBER) IS
  y NUMBER;
BEGIN
  y := x;
  DECLARE x NUMBER; BEGIN x := y; END;
  FOR i IN 1..3 LOOP y := i; END LOOP;
  UPDATE t SET x = x;
  y := x;
END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	// Match every occurrence using its actual source position, so a missing or
	// misplaced reference fails even if a symbol exists elsewhere in the file.
	got := map[[2]int32]*scip.Occurrence{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol != "" {
			got[[2]int32{o.Range[0], o.Range[1]}] = o
		}
	}
	checks := []struct {
		line, col int32
		sym       string
		role      scip.SymbolRole
	}{
		{1, 22, "scip-plsql oracle demo 1 HR/P().(X)", scip.SymbolRole_Definition},
		{2, 2, "local 0", scip.SymbolRole_Definition},
		{4, 2, "local 0", scip.SymbolRole_WriteAccess}, {4, 7, "scip-plsql oracle demo 1 HR/P().(X)", scip.SymbolRole_ReadAccess},
		{5, 10, "local 1", scip.SymbolRole_Definition}, {5, 26, "local 1", scip.SymbolRole_WriteAccess}, {5, 31, "local 0", scip.SymbolRole_ReadAccess},
		{6, 6, "local 2", scip.SymbolRole_Definition}, {6, 21, "local 0", scip.SymbolRole_WriteAccess}, {6, 26, "local 2", scip.SymbolRole_ReadAccess},
		{7, 15, "scip-plsql oracle demo 1 HR/T#X.", scip.SymbolRole_WriteAccess}, {7, 19, "scip-plsql oracle demo 1 HR/T#X.", scip.SymbolRole_ReadAccess},
		{8, 2, "local 0", scip.SymbolRole_WriteAccess}, {8, 7, "scip-plsql oracle demo 1 HR/P().(X)", scip.SymbolRole_ReadAccess},
	}
	for _, c := range checks {
		o := got[[2]int32{c.line, c.col}]
		if o == nil || o.Symbol != c.sym || o.SymbolRoles != int32(c.role) {
			t.Errorf("%d:%d: got %v want %s role %v", c.line, c.col, o, c.sym, c.role)
		}
	}
}

func TestPackageParametersShareSymbol(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PACKAGE hr.pkg AS
  PROCEDURE run(p NUMBER);
END;
/
CREATE PACKAGE BODY hr.pkg AS
  PROCEDURE run(p NUMBER) IS BEGIN p := p + 1; END;
END;
/
`
	if err := os.WriteFile(filepath.Join(root, "pkg.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	symbol := "scip-plsql oracle demo 1 HR/PKG#RUN().(P)"
	var roles []int32
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == symbol {
			roles = append(roles, o.SymbolRoles)
		}
	}
	expected := []int32{int32(scip.SymbolRole_ForwardDefinition), int32(scip.SymbolRole_Definition), int32(scip.SymbolRole_WriteAccess), int32(scip.SymbolRole_ReadAccess)}
	if len(roles) != len(expected) {
		t.Fatalf("parameter roles %v", roles)
	}
	for i := range roles {
		if roles[i] != expected[i] {
			t.Fatalf("parameter roles %v", roles)
		}
	}
}

func TestUpdateDoesNotCaptureSubqueryColumns(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.t (x NUMBER);
CREATE TABLE hr.other_t (x NUMBER);
CREATE PROCEDURE hr.p IS BEGIN
 UPDATE t SET x = (SELECT x FROM other_t);
END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 3 && o.Range[1] == 26 && o.Symbol == "scip-plsql oracle demo 1 HR/T#X." {
			t.Fatalf("subquery column captured by UPDATE target: %v", o)
		}
	}
}

func TestSelectScopesAndAliases(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.emp (id NUMBER, salary NUMBER);
CREATE TABLE hr.dept (id NUMBER, budget NUMBER);
CREATE PROCEDURE hr.p IS BEGIN
 SELECT e.salary, d.budget, id FROM emp e JOIN dept d ON e.id = d.id
 WHERE e.salary > (SELECT salary FROM emp WHERE id = e.id);
END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	lines := strings.Split(sql, "\n")
	got := map[[2]int32]string{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol != "" {
			got[[2]int32{o.Range[0], o.Range[1]}] = o.Symbol
		}
	}
	checks := []struct {
		line       int
		substring  string
		occurrence int
		symbol     string
	}{
		{3, "salary", 0, "HR/EMP#SALARY."}, {3, "budget", 0, "HR/DEPT#BUDGET."}, {3, "id", 0, ""},
		{3, "emp", 0, "HR/EMP#"}, {3, "dept", 0, "HR/DEPT#"}, {3, "id", 1, "HR/EMP#ID."}, {3, "id", 2, "HR/DEPT#ID."},
		{4, "salary", 0, "HR/EMP#SALARY."}, {4, "salary", 1, "HR/EMP#SALARY."}, {4, "id", 0, "HR/EMP#ID."}, {4, "id", 1, "HR/EMP#ID."},
	}
	for _, c := range checks {
		offset := -1
		start := 0
		for j := 0; j <= c.occurrence; j++ {
			p := strings.Index(lines[c.line][start:], c.substring)
			if p < 0 {
				t.Fatalf("missing %q", c.substring)
			}
			offset = start + p
			start = offset + len(c.substring)
		}
		want := ""
		if c.symbol != "" {
			want = "scip-plsql oracle demo 1 " + c.symbol
		}
		if actual := got[[2]int32{int32(c.line), int32(offset)}]; actual != want {
			t.Errorf("%d:%d %s got %q want %q", c.line, offset, c.substring, actual, want)
		}
	}
}

func TestSelectCTEAndUnknownSourcesDoNotCaptureBaseTable(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.emp (id NUMBER);
CREATE PROCEDURE hr.p IS BEGIN
 WITH emp AS (SELECT id FROM hr.emp) SELECT id FROM emp;
 SELECT id FROM hr.emp e JOIN unknown_table u ON e.id = u.id;
END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	// The CTE's direct projection retains the underlying column symbol. An
	// unqualified column with an unknown joined source remains unresolved;
	// qualified e.id still resolves.
	lines := strings.Split(sql, "\n")
	inner, outer, qualified := false, false, false
	for _, o := range idx.Documents[0].Occurrences {
		if len(o.Diagnostics) > 0 {
			t.Fatalf("unexpected parse diagnostic: %v", o.Diagnostics)
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#ID." && o.Range[0] == 2 && int(o.Range[1]) == strings.Index(lines[2], "id") {
			inner = true
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#ID." && o.Range[0] == 2 && int(o.Range[1]) == strings.LastIndex(lines[2], "id") {
			outer = true
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#ID." && o.Range[0] == 3 && int(o.Range[1]) == strings.Index(lines[3], "e.id")+2 {
			qualified = true
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#ID." && o.Range[0] == 3 && int(o.Range[1]) == strings.Index(lines[3], "id") {
			t.Errorf("ambiguous unknown source captured: %v", o)
		}
	}
	if !inner || !outer || !qualified {
		t.Fatalf("expected CTE input, projected output, and qualified columns: inner=%v outer=%v qualified=%v", inner, outer, qualified)
	}
}

func TestSelectIntoWritesLocal(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.emp (salary NUMBER);
CREATE PROCEDURE hr.p IS salary NUMBER; BEGIN
 SELECT salary INTO salary FROM emp;
END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	got := map[int32]*scip.Occurrence{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 2 {
			got[o.Range[1]] = o
		}
	}
	col, local := got[8], got[20]
	if col == nil || col.Symbol != "scip-plsql oracle demo 1 HR/EMP#SALARY." || col.SymbolRoles != int32(scip.SymbolRole_ReadAccess) {
		t.Errorf("column: %v", col)
	}
	if local == nil || local.Symbol != "local 0" || local.SymbolRoles != int32(scip.SymbolRole_WriteAccess) {
		t.Errorf("INTO: %v", local)
	}
}

func TestSelectQualifiedNamesWithWhitespace(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr . emp (id NUMBER);
SELECT e . id FROM hr . emp e;
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	got := map[string]int{}
	for _, o := range idx.Documents[0].Occurrences {
		got[o.Symbol]++
	}
	if got["scip-plsql oracle demo 1 HR/EMP#"] != 2 || got["scip-plsql oracle demo 1 HR/EMP#ID."] != 2 {
		t.Fatalf("qualified names: %v", got)
	}
}

func TestInsertColumnWritesAndSelectReads(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.src (id NUMBER);
CREATE TABLE hr.dst (id NUMBER);
INSERT INTO hr.dst (id) SELECT id FROM hr.src;
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	got := map[string]int32{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 2 {
			got[o.Symbol] = o.SymbolRoles
		}
	}
	if got["scip-plsql oracle demo 1 HR/DST#ID."] != int32(scip.SymbolRole_WriteAccess) || got["scip-plsql oracle demo 1 HR/SRC#ID."] != int32(scip.SymbolRole_ReadAccess) {
		t.Fatalf("insert/select roles: %v", got)
	}
}

func TestSynonymsResolveAcrossFilesAndSchemas(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a-use.sql":      "SELECT p.id FROM people p;\nSELECT x.id FROM shared x;\nSELECT z.id FROM hr.shared z;\nSELECT c.id FROM cyclic_a c;\n",
		"b-synonyms.sql": "CREATE PUBLIC SYNONYM people FOR hr.second_table;\nCREATE SYNONYM app.people FOR hr.first_table;\nCREATE PUBLIC SYNONYM shared FOR hr.second_table;\nCREATE SYNONYM app.cyclic_a FOR app.cyclic_b;\nCREATE SYNONYM app.cyclic_b FOR app.cyclic_a;\n",
		"z-tables.sql":   "CREATE TABLE hr.first_table (id NUMBER);\nCREATE TABLE hr.second_table (id NUMBER);\n",
	}
	for path, text := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP"}); err != nil {
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
	got := map[[2]int32]string{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol != "" {
			got[[2]int32{o.Range[0], o.Range[1]}] = o.Symbol
		}
	}
	first := "scip-plsql oracle demo 1 HR/FIRST_TABLE#"
	second := "scip-plsql oracle demo 1 HR/SECOND_TABLE#"
	checks := map[[2]int32]string{{0, 9}: first + "ID.", {0, 17}: first, {1, 9}: second + "ID.", {1, 17}: second, {2, 9}: "", {2, 25}: "", {3, 9}: "", {3, 17}: ""}
	for pos, want := range checks {
		if got[pos] != want {
			t.Errorf("%v got %q want %q; all %v", pos, got[pos], want, got)
		}
	}
	relations := map[string]string{}
	for _, info := range idx.Documents[1].Symbols {
		for _, r := range info.Relationships {
			if r.IsReference {
				relations[info.Symbol] = r.Symbol
			}
		}
	}
	if relations["scip-plsql oracle demo 1 APP/PEOPLE#"] != first || relations["scip-plsql oracle demo 1 PUBLIC/PEOPLE#"] != second {
		t.Errorf("synonym relationships: %v", relations)
	}
}

func TestSynonymChainsAndUnqualifiedTargets(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.base (id NUMBER);
CREATE SYNONYM hr.a FOR base;
CREATE SYNONYM hr.b FOR hr.a;
SELECT id FROM b;
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	table, col := false, false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 3 {
			table = table || o.Symbol == "scip-plsql oracle demo 1 HR/BASE#"
			col = col || o.Symbol == "scip-plsql oracle demo 1 HR/BASE#ID."
		}
	}
	if !table || !col {
		t.Fatalf("chain table=%v column=%v", table, col)
	}
}

func TestRemoteSynonymDoesNotPointToLocalTable(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.emp (id NUMBER);
CREATE SYNONYM hr.remote_emp FOR hr.emp@remote_db;
SELECT id FROM remote_emp;
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] > 0 && strings.HasPrefix(o.Symbol, "scip-plsql oracle demo 1 HR/EMP#") {
			t.Errorf("remote target resolved locally: %v", o)
		}
	}
	const remoteTarget = "scip-plsql oracle DBLINK_52454D4F54455F4442 1 HR/EMP#"
	var externalTarget, synonymRelationship bool
	for _, info := range idx.ExternalSymbols {
		externalTarget = externalTarget || info.Symbol == remoteTarget
	}
	for _, doc := range idx.Documents {
		for _, info := range doc.Symbols {
			if info.Symbol != "scip-plsql oracle demo 1 HR/REMOTE_EMP#" {
				continue
			}
			for _, rel := range info.Relationships {
				synonymRelationship = synonymRelationship || rel.Symbol == remoteTarget && rel.IsReference
			}
		}
	}
	if !externalTarget || !synonymRelationship {
		t.Errorf("remote synonym target relationship missing: external=%t relationship=%t", externalTarget, synonymRelationship)
	}
}

func TestDatabaseLinkReferencesUseExternalSCIPPackage(t *testing.T) {
	root := t.TempDir()
	sql := `SELECT id FROM hr.emp@remote_db;
INSERT INTO hr.emp@remote_db (id) VALUES (1);
BEGIN hr.pkg.proc@remote_db; END;
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	const remotePrefix = "scip-plsql oracle DBLINK_52454D4F54455F4442 1 HR/"
	var remoteTable, remoteMethod bool
	for _, info := range idx.ExternalSymbols {
		if info.Symbol == remotePrefix+"EMP#" && info.Kind == scip.SymbolInformation_Struct {
			remoteTable = true
		}
		if info.Symbol == remotePrefix+"PKG#PROC()." && info.Kind == scip.SymbolInformation_Method {
			remoteMethod = true
		}
	}
	if !remoteTable {
		t.Errorf("missing external remote table symbol %q", remotePrefix+"EMP#")
	}
	if !remoteMethod {
		t.Errorf("missing external remote package method symbol %q", remotePrefix+"PKG#PROC().")
	}
	var remoteRead, remoteWrite bool
	for _, doc := range idx.Documents {
		for _, occurrence := range doc.Occurrences {
			if strings.HasPrefix(occurrence.Symbol, remotePrefix) {
				remoteRead = remoteRead || occurrence.SymbolRoles&int32(scip.SymbolRole_ReadAccess) != 0
				remoteWrite = remoteWrite || occurrence.SymbolRoles&int32(scip.SymbolRole_WriteAccess) != 0
			}
		}
	}
	if !remoteRead || !remoteWrite {
		t.Errorf("remote table read/write roles missing: read=%t write=%t", remoteRead, remoteWrite)
	}
}

func TestPackageOverloadOrderAndCrossSchemaCalls(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a_calls.sql": `CREATE PROCEDURE app.client IS BEGIN
 hr.pkg.run(123);
 hr.pkg.run('x');
END;
/
`,
		"b_body.sql": `CREATE PACKAGE BODY hr.pkg AS
 PROCEDURE run(p VARCHAR2) IS BEGIN NULL; END;
 PROCEDURE run(p NUMBER) IS BEGIN NULL; END;
END;
/
`,
		"z_spec.sql": `CREATE PACKAGE hr.pkg AS
 PROCEDURE run(p NUMBER);
 PROCEDURE run(p VARCHAR2);
END;
/
`,
	}
	for path, text := range files {
		if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP"}); err != nil {
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
	one := "scip-plsql oracle demo 1 HR/PKG#RUN()."
	two := "scip-plsql oracle demo 1 HR/PKG#RUN(+1)."
	got := map[[2]int32]string{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol != "" {
			got[[2]int32{o.Range[0], o.Range[1]}] = o.Symbol
		}
	}
	for pos, want := range map[[2]int32]string{{1, 4}: "scip-plsql oracle demo 1 HR/PKG#", {1, 8}: one, {2, 4}: "scip-plsql oracle demo 1 HR/PKG#", {2, 8}: two} {
		if got[pos] != want {
			t.Errorf("call %v got %q want %q", pos, got[pos], want)
		}
	}
	defs := map[int32]string{}
	for _, o := range idx.Documents[1].Occurrences {
		if o.SymbolRoles == int32(scip.SymbolRole_Definition) && strings.Contains(o.Symbol, "#RUN(") && strings.HasSuffix(o.Symbol, ").") {
			defs[o.Range[0]] = o.Symbol
		}
	}
	if defs[1] != two || defs[2] != one {
		t.Errorf("body overload symbols: %v", defs)
	}
}

func TestPackageOverloadNamedArgumentsAndAmbiguity(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a_call.sql": `CREATE PROCEDURE hr.client IS v NUMBER; BEGIN
 pkg.run(right_arg => 3);
 pkg.run(v);
END;
/
`,
		"b_pkg.sql": `CREATE PACKAGE hr.pkg AS
 PROCEDURE run(left_arg VARCHAR2);
 PROCEDURE run(right_arg NUMBER);
END;
/
`,
	}
	for p, tgt := range files {
		if err := os.WriteFile(filepath.Join(root, p), []byte(tgt), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	got := map[int32][]*scip.Occurrence{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[1] == 5 && strings.Contains(o.Symbol, "#RUN(") {
			got[o.Range[0]] = append(got[o.Range[0]], o)
		}
	}
	if len(got[1]) != 1 || got[1][0].Symbol != "scip-plsql oracle demo 1 HR/PKG#RUN(+1)." {
		t.Errorf("named call: %v", got[1])
	}
	if len(got[2]) != 2 {
		t.Errorf("ambiguous call: %v", got[2])
	} else {
		for _, o := range got[2] {
			if len(o.Diagnostics) != 1 || o.Diagnostics[0].Code != "ambiguous-overload" {
				t.Errorf("missing ambiguity: %v", o)
			}
		}
	}
}

func TestLocalQualifierShadowsPackage(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PACKAGE hr.pkg AS PROCEDURE run; END;
/
CREATE PROCEDURE hr.client IS pkg NUMBER; BEGIN pkg.run(); END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 2 && strings.Contains(o.Symbol, "HR/PKG#") {
			t.Errorf("local qualifier misresolved as package: %v", o)
		}
	}
}

func TestPackageCallsWithDefaultsAndPrivateMembers(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PACKAGE hr.pkg AS
 PROCEDURE run(a NUMBER, b VARCHAR2 DEFAULT 'x');
END;
/
CREATE PACKAGE BODY hr.pkg AS
 PROCEDURE private_call IS BEGIN NULL; END;
 PROCEDURE run(a NUMBER, b VARCHAR2) IS BEGIN private_call(); END;
END;
/
CREATE PROCEDURE hr.client IS BEGIN pkg.run(2); pkg.private_call(); END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	public, private, externalPrivate := false, false, false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 9 && o.Symbol == "scip-plsql oracle demo 1 HR/PKG#RUN()." {
			public = true
		}
		if o.Range[0] == 6 && o.Symbol == "scip-plsql oracle demo 1 HR/PKG#PRIVATE_CALL()." {
			private = true
		}
		if o.Range[0] == 9 && o.Symbol == "scip-plsql oracle demo 1 HR/PKG#PRIVATE_CALL()." {
			externalPrivate = true
		}
	}
	if !public || !private || externalPrivate {
		t.Fatalf("public %v private %v external private %v", public, private, externalPrivate)
	}
}

func TestPackageProcedureCallWithoutParentheses(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PACKAGE hr.pkg AS PROCEDURE run; END;
/
CREATE PROCEDURE hr.client IS BEGIN pkg.run; END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
	found := false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 2 && o.Symbol == "scip-plsql oracle demo 1 HR/PKG#RUN()." && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
			found = true
		}
	}
	if !found {
		t.Fatal("missing no-argument call reference")
	}
}

func TestQualifiedStandaloneCall(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PROCEDURE hr.recalc IS BEGIN NULL; END;
/
CREATE PROCEDURE app.client IS BEGIN hr.recalc(); END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "APP"}); err != nil {
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
	found := false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Range[0] == 2 && o.Symbol == "scip-plsql oracle demo 1 HR/RECALC()." && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
			found = true
		}
	}
	if !found {
		t.Fatal("missing cross-schema standalone call")
	}
}
