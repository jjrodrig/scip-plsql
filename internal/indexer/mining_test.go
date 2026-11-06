package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestMiningWildcardPreservesOffsetsAndAvoidsFalseColumn(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE emp (model NUMBER, id NUMBER);\nSELECT prediction(model USING *), id FROM emp;\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	var metrics Metrics
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", Metrics: &metrics}); err != nil {
		t.Fatal(err)
	}
	if metrics.DegradedUnits != 0 {
		t.Fatalf("degraded: %+v", metrics)
	}
	data, err := os.ReadFile(filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	var modelRef, idRef, warning bool
	for _, o := range idx.Documents[0].Occurrences {
		if len(o.Range) > 0 && o.Range[0] == 1 && o.Symbol == "scip-plsql oracle demo 1 HR/EMP#MODEL." {
			modelRef = true
		}
		if len(o.Range) > 0 && o.Range[0] == 1 && o.Symbol == "scip-plsql oracle demo 1 HR/EMP#ID." {
			idRef = true
		}
		for _, d := range o.Diagnostics {
			if d.Code == "mining-model-unresolved" {
				warning = true
			}
		}
	}
	if modelRef || !idRef || !warning {
		t.Fatalf("modelRef=%v idRef=%v warning=%v", modelRef, idRef, warning)
	}
}

func TestMiningWildcardIgnoresLiteralsCommentsAndQualifiedFunctions(t *testing.T) {
	input := "-- prediction(m USING *)\n  REM prediction(m USING *)\nSELECT 'prediction(m USING *)', pkg.prediction(m USING *), prediction_probability(\"HR\".\"MODEL\", 1 USING *) FROM t;"
	out, diagnostics := preprocessMining(input)
	if len(out) != len(input) || len(diagnostics) != 1 {
		t.Fatalf("length=%d diagnostics=%v", len(out), diagnostics)
	}
	if !strings.Contains(out, "pkg.prediction(m USING *)") || !strings.Contains(out, "'prediction(m USING *)'") {
		t.Fatalf("rewrote non-mining code: %s", out)
	}
	if _, err := parseRange("SELECT prediction_probability(0, 1, *) FROM t;", 0, len("SELECT prediction_probability(0, 1, *) FROM t;")); err != nil {
		t.Fatal(err)
	}
}

func TestMiningUsingColumnsResolveInSQLScope(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE emp (model NUMBER, id NUMBER, score NUMBER);\nSELECT prediction(model USING id, emp.score) FROM emp;\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	var metrics Metrics
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", Metrics: &metrics}); err != nil {
		t.Fatal(err)
	}
	if metrics.DegradedUnits != 0 {
		t.Fatalf("degraded: %+v", metrics)
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
		if len(o.Range) > 0 && o.Range[0] == 1 {
			found[o.Symbol] = true
		}
	}
	if !found["scip-plsql oracle demo 1 HR/EMP#ID."] || !found["scip-plsql oracle demo 1 HR/EMP#SCORE."] || found["scip-plsql oracle demo 1 HR/EMP#MODEL."] {
		t.Fatalf("unexpected references: %v", found)
	}
}

func TestMiningUsingAliasesAndCostModel(t *testing.T) {
	input := "SELECT prediction_probability(m, 1 USING 44 AS age, 'AS no' AS label, emp.score AS x), prediction(m COST MODEL USING *) FROM emp;"
	out, diagnostics := preprocessMining(input)
	if len(out) != len(input) || len(diagnostics) != 2 {
		t.Fatalf("output=%q diagnostics=%v", out, diagnostics)
	}
	if !strings.Contains(out, "'AS no'") {
		t.Fatal("changed string literal")
	}
	if _, err := parseRange(out, 0, len(out)); err != nil {
		t.Fatalf("surrogate did not parse: %v\n%s", err, out)
	}
}

func TestMiningInlineAnomalyAndCostAfterClass(t *testing.T) {
	input := "SELECT prediction_probability(OF ANOMALY, 0 USING *) OVER (PARTITION BY id), prediction_cost(m, 1 COST MODEL USING *) FROM emp;"
	out, diagnostics := preprocessMining(input)
	if len(out) != len(input) || len(diagnostics) != 2 {
		t.Fatalf("output=%q diagnostics=%v", out, diagnostics)
	}
	if _, err := parseRange(out, 0, len(out)); err != nil {
		t.Fatalf("surrogate did not parse: %v\n%s", err, out)
	}
}

func TestMiningDoesNotRewriteCostInClassLiteral(t *testing.T) {
	input := "SELECT prediction_probability(m, 'COST MODEL' USING *) FROM emp;"
	out, ds := preprocessMining(input)
	if len(ds) != 1 || !strings.Contains(out, "'COST MODEL'") {
		t.Fatalf("output=%q diagnostics=%v", out, ds)
	}
	if _, err := parseRange(out, 0, len(out)); err != nil {
		t.Fatal(err)
	}
}

func TestMiningFeatureAliasIsNotResolvedAsColumn(t *testing.T) {
	root := t.TempDir()
	sql := "CREATE TABLE emp (score NUMBER, id NUMBER);\nSELECT prediction(m USING 44 AS score, id AS feature) FROM emp;\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
		t.Fatal(err)
	}
	var metrics Metrics
	if err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", Metrics: &metrics}); err != nil {
		t.Fatal(err)
	}
	if metrics.DegradedUnits != 0 {
		t.Fatalf("degraded: %+v", metrics)
	}
	b, err := os.ReadFile(filepath.Join(root, "index.scip"))
	if err != nil {
		t.Fatal(err)
	}
	var idx scip.Index
	if err := proto.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	var idRef, aliasRef bool
	for _, o := range idx.Documents[0].Occurrences {
		if len(o.Range) > 0 && o.Range[0] == 1 {
			if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#ID." {
				idRef = true
			}
			if o.Symbol == "scip-plsql oracle demo 1 HR/EMP#SCORE." {
				aliasRef = true
			}
		}
	}
	if !idRef || aliasRef {
		t.Fatalf("id reference=%v alias incorrectly resolved=%v", idRef, aliasRef)
	}
}
