package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestWrappedHeaderDoesNotConsumeFollowingUnits(t *testing.T) {
	root := t.TempDir()
	sql := `-- wrapped body
CREATE OR REPLACE PACKAGE BODY hr.pkg wrapped
0
abcd abcd
xyz;
/
CREATE TABLE hr.after_wrapped (id NUMBER);
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
	symbols := map[string]*scip.Occurrence{}
	wrapped := false
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol != "" {
			symbols[o.Symbol] = o
		}
		for _, d := range o.Diagnostics {
			if d.Code == "wrapped" {
				wrapped = true
			} else {
				t.Errorf("unexpected diagnostic %v", d)
			}
		}
	}
	def := symbols["scip-plsql oracle demo 1 HR/PKG#"]
	if def == nil || def.SymbolRoles != int32(scip.SymbolRole_Definition) || def.Range[0] != 1 {
		t.Errorf("wrapped definition %v", def)
	}
	if symbols["scip-plsql oracle demo 1 HR/AFTER_WRAPPED#ID."] == nil {
		t.Errorf("missing following declaration: %v", symbols)
	}
	if !wrapped {
		t.Error("missing wrapped diagnostic")
	}
}
func TestParseFailureAndBudgetKeepHeaders(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.broken (id NUMBER, );
CREATE PROCEDURE hr.large IS BEGIN NULL; NULL; NULL; END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", MaxTokens: 12}); err != nil {
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
	symbols, codes := map[string]bool{}, map[string]bool{}
	for _, o := range idx.Documents[0].Occurrences {
		symbols[o.Symbol] = true
		for _, d := range o.Diagnostics {
			codes[d.Code] = true
		}
	}
	for _, sym := range []string{"scip-plsql oracle demo 1 HR/BROKEN#", "scip-plsql oracle demo 1 HR/LARGE()."} {
		if !symbols[sym] {
			t.Errorf("missing degraded header %s", sym)
		}
	}
	if !codes["parse-error"] || !codes["token-budget"] {
		t.Errorf("diagnostics: %v", codes)
	}
}
func TestWrappedTextInLiteralsAndCommentsIsNotAUnit(t *testing.T) {
	input := `SELECT 'CREATE PACKAGE BODY hr.fake wrapped' FROM dual;
-- CREATE PACKAGE fake wrapped
SELECT q'[CREATE PACKAGE BODY hr.fake wrapped]' FROM dual;
`
	out, units := wrappedUnits(input)
	if len(units) != 0 || out != input {
		t.Fatalf("wrapped scanner modified literals: %q %v", out, units)
	}
}
func FuzzWrappedOffsets(f *testing.F) {
	for _, s := range []string{"CREATE PACKAGE BODY p wrapped\nabc\n/\nSELECT 1 FROM dual;", "SELECT 'CREATE PACKAGE p wrapped' FROM dual;"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, units := wrappedUnits(s)
		if len(out) != len(s) {
			t.Fatal("length changed")
		}
		for i := range s {
			if (s[i] == '\n' || s[i] == '\r') && s[i] != out[i] {
				t.Fatal("newline changed")
			}
		}
		for _, u := range units {
			if u.start < 0 || u.end > len(s) || u.start >= u.end || u.header == nil || !strings.EqualFold(s[u.header.wrappedStart:u.header.wrappedStart+7], "wrapped") {
				t.Fatalf("invalid unit %v", u)
			}
		}
	})
}

func TestDegradedReferencesIgnoreStringsAndComments(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE TABLE hr.target (id NUMBER);
CREATE PROCEDURE hr.broken IS BEGIN
 SELECT id INTO x FROM hr.target;
 -- FROM hr.target
 x := 'FROM hr.target';
 invalid syntax tokens;
END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", MaxTokens: 20}); err != nil {
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
	got := []int32{}
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1 HR/TARGET#" && o.SymbolRoles == int32(scip.SymbolRole_ReadAccess) {
			got = append(got, o.Range[0])
		}
	}
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("degraded references: %v", got)
	}
}

func TestParseErrorAfterWrappedPointsToFollowingUnit(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE PACKAGE BODY hr.pkg wrapped\nabc\n/\nCREATE TABLE hr.bad (x NUMBER, );\n"
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
		for _, d := range o.Diagnostics {
			if d.Code == "parse-error" {
				found = true
				if o.Range[0] != 3 {
					t.Errorf("parse diagnostic points to line %d", o.Range[0])
				}
			}
		}
	}
	if !found {
		t.Fatal("missing parse error")
	}
}
