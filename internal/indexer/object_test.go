package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestObjectTypeMembersBodiesAndInheritance(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a_body.sql": `CREATE TYPE BODY hr.address_t AS
 MEMBER FUNCTION label(p VARCHAR2) RETURN VARCHAR2 IS BEGIN RETURN street; END;
 MEMBER FUNCTION label(p NUMBER) RETURN VARCHAR2 IS BEGIN RETURN street; END;
END;
/
`,
		"z_spec.sql": `CREATE TYPE hr.address_t AS OBJECT (
 street VARCHAR2(100),
 MEMBER FUNCTION label(p NUMBER) RETURN VARCHAR2,
 MEMBER FUNCTION label(p VARCHAR2) RETURN VARCHAR2
) NOT FINAL;
/
CREATE TYPE hr.sub_t UNDER hr.address_t (extra NUMBER);
/
`,
	}
	for file, text := range files {
		if err := os.WriteFile(filepath.Join(root, file), []byte(text), 0600); err != nil {
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
	body, spec := idx.Documents[0], idx.Documents[1]
	wantNumber := "scip-plsql oracle demo 1 HR/ADDRESS_T#LABEL()."
	wantString := "scip-plsql oracle demo 1 HR/ADDRESS_T#LABEL(+1)."
	wantField := "scip-plsql oracle demo 1 HR/ADDRESS_T#STREET."
	for _, tc := range []struct {
		doc  *scip.Document
		line int32
		sym  string
		role scip.SymbolRole
	}{{spec, 0, "scip-plsql oracle demo 1 HR/ADDRESS_T#", scip.SymbolRole_ForwardDefinition}, {spec, 1, wantField, scip.SymbolRole_ForwardDefinition}, {spec, 2, wantNumber, scip.SymbolRole_ForwardDefinition}, {spec, 3, wantString, scip.SymbolRole_ForwardDefinition}, {body, 0, "scip-plsql oracle demo 1 HR/ADDRESS_T#", scip.SymbolRole_Definition}, {body, 1, wantString, scip.SymbolRole_Definition}, {body, 2, wantNumber, scip.SymbolRole_Definition}, {body, 1, wantField, scip.SymbolRole_ReadAccess}, {body, 2, wantField, scip.SymbolRole_ReadAccess}} {
		found := false
		for _, o := range tc.doc.Occurrences {
			if o.Range[0] == tc.line && o.Symbol == tc.sym && o.SymbolRoles == int32(tc.role) {
				found = true
			}
		}
		if !found {
			t.Errorf("missing line %d %s role %v in %s: %v", tc.line, tc.sym, tc.role, tc.doc.RelativePath, tc.doc.Occurrences)
		}
	}
	found := false
	for _, info := range spec.Symbols {
		if info.Symbol == "scip-plsql oracle demo 1 HR/SUB_T#" {
			for _, rel := range info.Relationships {
				if rel.Symbol == "scip-plsql oracle demo 1 HR/ADDRESS_T#" && rel.IsImplementation {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("missing UNDER implementation relationship")
	}
}

func TestObjectSelfInheritedFieldsAndCalls(t *testing.T) {
	sql := `CREATE TYPE hr.base_t AS OBJECT (amount NUMBER, MEMBER FUNCTION label RETURN VARCHAR2) NOT FINAL;
/
CREATE TYPE hr.child_t UNDER hr.base_t (extra NUMBER, MEMBER PROCEDURE adjust);
/
CREATE TYPE BODY hr.child_t AS
 MEMBER PROCEDURE adjust IS BEGIN SELF.amount := extra; END;
END;
/
CREATE PROCEDURE hr.client IS r hr.child_t; v NUMBER; BEGIN r.amount := 1; r.adjust(); v := r.amount; END;
/
`
	r, w := scip.SymbolRole_ReadAccess, scip.SymbolRole_WriteAccess
	checkDML(t, sql, []expectedDML{
		{5, "amount", 0, "HR/BASE_T#AMOUNT.", w}, {5, "extra", 0, "HR/CHILD_T#EXTRA.", r},
		{8, "child_t", 0, "HR/CHILD_T#", r}, {8, "amount", 0, "HR/BASE_T#AMOUNT.", w}, {8, "adjust", 0, "HR/CHILD_T#ADJUST().", r}, {8, "amount", 1, "HR/BASE_T#AMOUNT.", r},
	})
}

func TestSelfMethodAndInheritedMethodCalls(t *testing.T) {
	sql := `CREATE TYPE hr.base_t AS OBJECT (x NUMBER, MEMBER PROCEDURE run) NOT FINAL;
/
CREATE TYPE hr.child_t UNDER hr.base_t (MEMBER PROCEDURE call_self);
/
CREATE TYPE BODY hr.child_t AS MEMBER PROCEDURE call_self IS BEGIN SELF.call_self(); END; END;
/
CREATE PROCEDURE hr.client IS r hr.child_t; BEGIN r.run(); END;
/
`
	r := scip.SymbolRole_ReadAccess
	checkDML(t, sql, []expectedDML{{4, "call_self", 1, "HR/CHILD_T#CALL_SELF().", r}, {6, "run", 0, "HR/BASE_T#RUN().", r}})
}

func TestExternalTypeAttributesAvailableToBody(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TYPE BODY hr.address_t AS MEMBER FUNCTION label RETURN VARCHAR2 IS BEGIN RETURN street; END; END;
/
`
	snapshot := `{"objects":[{"schema":"HR","name":"ADDRESS_T","kind":"type","columns":[{"name":"STREET","type":"VARCHAR2"}]}]}`
	for p, data := range map[string]string{"a.sql": sql, "dictionary.json": snapshot} {
		if err := os.WriteFile(filepath.Join(root, p), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Catalog: "dictionary.json", Database: "demo", Version: "1", Schema: "HR"}); err != nil {
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
		if o.Symbol == "scip-plsql oracle demo 1 HR/ADDRESS_T#STREET." && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
			found = true
		}
	}
	if !found {
		t.Errorf("missing external attribute reference: %v", idx.Documents[0].Occurrences)
	}
}

func TestObjectProcedureCallsWithoutParentheses(t *testing.T) {
	sql := `CREATE TYPE hr.obj_t AS OBJECT (x NUMBER, MEMBER PROCEDURE run, MEMBER PROCEDURE call_self);
/
CREATE TYPE BODY hr.obj_t AS MEMBER PROCEDURE call_self IS BEGIN SELF.run; END; END;
/
CREATE PROCEDURE hr.client IS r hr.obj_t; BEGIN r.run; END;
/
`
	r := scip.SymbolRole_ReadAccess
	checkDML(t, sql, []expectedDML{{2, "run", 0, "HR/OBJ_T#RUN().", r}, {4, "run", 0, "HR/OBJ_T#RUN().", r}})
}
