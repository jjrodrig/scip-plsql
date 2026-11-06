package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
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

func TestUnresolvedSubstitutionsExcludeCommentsAndInactiveBranches(t *testing.T) {
	input := "-- &comment\nREM &remark\nSELECT '&literal', &&active FROM dual;\n$IF $$enabled $THEN\nSELECT &inactive FROM dual;\n$END\n"
	processed, _ := preprocess(input, map[string]string{"enabled": "false"})
	got := unresolvedSubstitutions(input, processed)
	if len(got) != 1 || input[got[0].start:got[0].end] != "&&active" {
		t.Fatalf("diagnostics: %+v", got)
	}
}

func TestUnknownSynonymModifierRetainsDiagnostic(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE &action_type. SYNONYM hr.alias FOR hr.target;\n"
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
	var warning, parseError bool
	for _, o := range idx.Documents[0].Occurrences {
		for _, d := range o.Diagnostics {
			warning = warning || d.Code == "substitution-unresolved"
			parseError = parseError || d.Code == "parse-error"
		}
	}
	if !warning || !parseError {
		t.Fatalf("substitution warning=%v parse error=%v", warning, parseError)
	}
}

func TestSQLPlusContinuationAndSubstitutionDelimiter(t *testing.T) {
	input := "INSERT INTO &owner..products VALUES(1-\n,'a-'||-\n'b');\n-- comment -\nSELECT q'[literal-\nvalue]' FROM dual;\nSELECT &part.suffix FROM dual;\n"
	want := "INSERT INTO xowner .products VALUES(1 \n,'a-'|| \n'b');\n-- comment -\nSELECT q'[literal-\nvalue]' FROM dual;\nSELECT xpartxsuffix FROM dual;\n"
	got, diagnostics := preprocess(input, nil)
	if got != want || len(diagnostics) != 0 {
		t.Fatalf("got %q, diagnostics %v; want %q", got, diagnostics, want)
	}
}

func TestIndexSQLPlusContinuedInsert(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE products (id NUMBER, name VARCHAR2(50));\nINSERT INTO products (id, name) VALUES(1-\n, 'a'||-\n'b');\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	var metrics Metrics
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", Metrics: &metrics}); err != nil {
		t.Fatal(err)
	}
	if metrics.DegradedUnits != 0 || metrics.ParsedUnits != 2 {
		t.Fatalf("metrics: %+v", metrics)
	}
	b, err := os.ReadFile(filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	var references int
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1 HR/PRODUCTS#ID." && o.SymbolRoles&int32(scip.SymbolRole_WriteAccess) != 0 {
			references++
		}
	}
	if references != 1 {
		t.Fatalf("expected one write to products.id, got %d", references)
	}
}

func TestContinuedStringInsideSQLPlusInsert(t *testing.T) {
	input := "rem Oracle's language\nINSERT INTO products VALUES (1726-\n, 'US'-\n, UNISTR(-\n'LCD Monitor'-\n), UNISTR(-\n'Liquid display,-\nhigh resolution.'-\n));\n"
	out, _ := preprocess(input, nil)
	start := strings.Index(out, "INSERT")
	list, err := parseRange(out, start, len(out))
	if err != nil || list == nil {
		t.Fatalf("continued literal must parse: %v\n%s", err, out)
	}
}

func TestContinuedDynamicSQLPreservesReferenceRanges(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE emp (id NUMBER);\nBEGIN\n  EXECUTE IMMEDIATE 'SELECT id-\n FROM em-\np';\nEND;\n/\n"
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
	var table, col bool
	for _, o := range idx.Documents[0].Occurrences {
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#" && len(o.Range) == 4 && o.Range[0] == 3 && o.Range[2] == 4 {
			table = true
		}
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#ID." && len(o.Range) == 3 && o.Range[0] == 2 && o.Range[2]-o.Range[1] == 2 {
			col = true
		}
		for _, d := range o.Diagnostics {
			if strings.HasPrefix(d.Code, "dynamic-sql-") {
				t.Errorf("unexpected diagnostic: %v", d)
			}
		}
	}
	if !table || !col {
		t.Fatalf("table=%v column=%v", table, col)
	}
}

func TestContinuedAlternativeQuotedDynamicSQL(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE emp (id NUMBER);\nBEGIN\n  EXECUTE IMMEDIATE q'[SELECT id FROM em-\np]';\nEND;\n/\n"
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
		if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#" && len(o.Range) == 4 && o.Range[0] == 2 && o.Range[2] == 3 {
			return
		}
	}
	t.Fatal("missing reference across continued q-quoted literal")
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
	found, warned := false, false
	for _, o := range idx.Documents[0].Occurrences {
		for _, d := range o.Diagnostics {
			if d.Code == "substitution-unresolved" {
				warned = true
			} else {
				t.Errorf("unexpected diagnostic: %v", d)
			}
		}
		if o.Range[0] == 1 && o.Range[1] == 28 && o.Symbol == "scip-plsql oracle demo 1 HR/EMP#" {
			found = true
		}
	}
	if !found || !warned {
		t.Fatalf("reference=%v unresolved substitution diagnostic=%v", found, warned)
	}
}
