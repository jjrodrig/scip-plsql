package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestPreprocessConditionalsAndOffsets(t *testing.T) {
	input := `CREATE PACKAGE hr.p AS
 $IF $$enabled AND ($$level >= 2) $THEN
   PROCEDURE selected;
 $ELSIF $$fallback $THEN
   PROCEDURE fallback;
 $ELSE
   PROCEDURE other;
 $END
 PROCEDURE tail;
END;
/
`
	for _, tc := range []struct {
		flags  map[string]string
		active string
	}{
		{map[string]string{"enabled": "true", "level": "2", "fallback": "true"}, "selected"},
		{map[string]string{"enabled": "false", "level": "2", "fallback": "true"}, "fallback"},
		{map[string]string{"enabled": "false", "level": "2", "fallback": "false"}, "other"},
	} {
		got, diagnostics := preprocess(input, tc.flags)
		if len(diagnostics) > 0 {
			t.Fatalf("diagnostics: %v", diagnostics)
		}
		if len(got) != len(input) {
			t.Fatal("byte length changed")
		}
		for i := range input {
			if (input[i] == '\n' || input[i] == '\r') && input[i] != got[i] {
				t.Fatalf("newline moved: %d", i)
			}
		}
		for _, id := range []string{"selected", "fallback", "other"} {
			if strings.Contains(got, id) != (id == tc.active) {
				t.Errorf("%s with %v: %s", id, tc.flags, got)
			}
		}
		if strings.Index(got, "PROCEDURE "+tc.active) != strings.Index(input, "PROCEDURE "+tc.active) {
			t.Fatal("position changed")
		}
	}
}
func TestPreprocessLiteralsCommentsAndSubstitutions(t *testing.T) {
	input := `-- &comment $IF $$x $THEN
SELECT '&literal', q'[&quoted $IF $$x $THEN]', "&identifier", &first, &&second FROM t; /* &comment */
`
	got, diagnostics := preprocess(input, nil)
	if len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	want := strings.Replace(input, "&first, &&second", "xfirst, xxsecond", 1)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
func TestPreprocessUnknownFlagDoesNotChooseElse(t *testing.T) {
	in := "$IF $$missing $THEN\nCREATE TABLE a (x NUMBER);\n$ELSE\nCREATE TABLE b (x NUMBER);\n$END\n"
	got, diags := preprocess(in, nil)
	if len(diags) != 1 || !strings.Contains(diags[0].message, "$$MISSING") {
		t.Fatalf("diagnostics: %v", diags)
	}
	if strings.Contains(got, "CREATE TABLE") {
		t.Fatalf("unknown condition selected a branch: %s", got)
	}
}
func TestIndexConditionalCompilation(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PACKAGE hr.p AS
 $IF $$feature $THEN
  PROCEDURE enabled;
 $ELSE
  PROCEDURE disabled;
 $END
END;
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", CCFlags: map[string]string{"feature": "true"}}); err != nil {
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
	got := map[string]*scip.Occurrence{}
	for _, o := range idx.Documents[0].Occurrences {
		if len(o.Diagnostics) > 0 {
			t.Errorf("unexpected diagnostic %v", o.Diagnostics)
		}
		got[o.Symbol] = o
	}
	yes := got["scip-plsql oracle demo 1 HR/P#ENABLED()."]
	if yes == nil || yes.Range[0] != 2 || yes.Range[1] != 12 {
		t.Errorf("enabled symbol/range %v", yes)
	}
	if got["scip-plsql oracle demo 1 HR/P#DISABLED()."] != nil {
		t.Error("inactive branch indexed")
	}
}
func FuzzPreprocessOffsets(f *testing.F) {
	for _, s := range []string{"$IF $$x $THEN\n&v\n$ELSE\nq'[x]'\n$END", "SELECT '&x' FROM t;"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, _ := preprocess(s, map[string]string{"x": "true"})
		if len(out) != len(s) {
			t.Fatal("changed length")
		}
		for i := range s {
			if (s[i] == '\n' || s[i] == '\r') && s[i] != out[i] {
				t.Fatal("changed line endings")
			}
		}
	})
}

func TestIndexUnknownConditionalProducesDiagnosticAndSkipsBranches(t *testing.T) {
	root := t.TempDir()
	sql := `CREATE PACKAGE hr.p AS
 $IF $$missing $THEN
  PROCEDURE enabled;
 $ELSE
  PROCEDURE disabled;
 $END
 PROCEDURE always_here;
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
	diag, always := false, false
	for _, o := range idx.Documents[0].Occurrences {
		for _, d := range o.Diagnostics {
			if d.Code == "conditional-compilation" {
				diag = true
			}
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/P#ALWAYS_HERE()." {
			always = true
		}
		if strings.Contains(o.Symbol, "ENABLED") || strings.Contains(o.Symbol, "DISABLED") {
			t.Errorf("unknown branch indexed: %v", o)
		}
	}
	if !diag || !always {
		t.Fatalf("diagnostic %v unaffected declaration %v", diag, always)
	}
}
func TestNestedConditional(t *testing.T) {
	input := `$IF $$outer $THEN
 $IF $$inner $THEN
  yes
 $ELSE
  no
 $END
$ELSE
 outside
$END
`
	got, diags := preprocess(input, map[string]string{"OUTER": "true", "INNER": "false"})
	if len(diags) != 0 || !strings.Contains(got, "no") || strings.Contains(got, "yes") || strings.Contains(got, "outside") {
		t.Fatalf("nested: %q %v", got, diags)
	}
}

func TestIndexSubstitutionPreservesFollowingReference(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE hr.emp (id NUMBER);\nSELECT &input_value FROM hr.emp;\n"
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
		if len(o.Diagnostics) > 0 {
			t.Errorf("unexpected diagnostic: %v", o.Diagnostics)
		}
		if o.Range[0] == 1 && o.Range[1] == 28 && o.Symbol == "scip-plsql oracle demo 1 HR/EMP#" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing reference after substitution")
	}
}
