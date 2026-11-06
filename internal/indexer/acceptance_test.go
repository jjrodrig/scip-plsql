package indexer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/sourcegraph/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// TestSCIPAcceptance uses Sourcegraph's CLI when available. Set SCIP_BIN to
// its path to make these checks mandatory in CI; otherwise a missing CLI skips.
func findSCIP(t *testing.T) string {
	t.Helper()
	executable := os.Getenv("SCIP_BIN")
	if executable == "" {
		var err error
		executable, err = exec.LookPath("scip")
		if err != nil {
			t.Skip("install scip or set SCIP_BIN to run official acceptance checks")
		}
	}
	executable, err := exec.LookPath(executable)
	if err != nil {
		t.Fatalf("SCIP_BIN: %v", err)
	}
	return executable
}

func TestSCIPAcceptance(t *testing.T) {
	executable := findSCIP(t)
	for _, tc := range []struct {
		name                   string
		files                  []string
		documents, occurrences int
		assertions             []string
	}{
		{"basic", []string{"body.sql", "schema.sql"}, 2, 13, []string{"7 assertions", "6 assertions"}},
		{"types", []string{"types.sql"}, 1, 12, []string{"11 assertions"}},
		{"object", []string{"object.sql"}, 1, 16, []string{"13 assertions"}},
		{"sqlplus", []string{"continuation.sql"}, 1, 6, []string{"6 assertions"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, err := filepath.Abs(filepath.Join("testdata", "accept", tc.name))
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "index.scip")
			if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR"}); err != nil {
				t.Fatal(err)
			}
			run := func(args ...string) []byte {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				data, err := exec.CommandContext(ctx, executable, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("scip %v: %v\n%s", args, err, data)
				}
				return data
			}
			run("lint", out)
			tests := run("test", "--from", out, "--comment-syntax=--", root)
			for _, a := range tc.assertions {
				if !bytes.Contains(tests, []byte(a)) {
					t.Fatalf("unexpected SCIP acceptance result: %s", tests)
				}
			}
			stats := run("stats", "--from", out, "--project-root", root)
			var result struct {
				Documents   int `json:"documents"`
				Occurrences int `json:"occurrences"`
			}
			if err := json.Unmarshal(stats, &result); err != nil || result.Documents != tc.documents || result.Occurrences != tc.occurrences {
				t.Fatalf("unexpected SCIP stats: %s (%v)", stats, err)
			}
			output := filepath.Join(t.TempDir(), "snapshots")
			run("snapshot", "--from", out, "--to", output, "--project-root", root, "--comment-syntax=--")
			for _, name := range tc.files {
				got, err := os.ReadFile(filepath.Join(output, name))
				if err != nil {
					t.Fatal(err)
				}
				want, err := os.ReadFile(filepath.Join("testdata", "golden", tc.name, name))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("snapshot mismatch for %s", name)
				}
			}
		})
	}
}

func TestSCIPLintDegradedDefinitions(t *testing.T) {
	executable := findSCIP(t)
	root := t.TempDir()
	source := `CREATE PROCEDURE hr.broken IS BEGIN invalid syntax !!; END;
/
CREATE PACKAGE BODY hr.hidden wrapped
abc
/
`
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, "lint", out).CombinedOutput()
	if err != nil {
		t.Fatalf("scip lint: %v\n%s", err, output)
	}
}

// SCIP permits an occurrence with only a diagnostic. Older scip lint versions
// incorrectly demand a symbol, so record that precise interoperability gap
// without inventing a definition at the diagnostic location.
func TestSCIPLintDiagnosticOnlyCompatibility(t *testing.T) {
	executable := findSCIP(t)
	root := t.TempDir()
	source := "CREATE PACKAGE hr.p AS\n$IF $$unknown $THEN\nPROCEDURE a;\n$END\nEND;\n/\n"
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR"}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var index scip.Index
	if err := proto.Unmarshal(encoded, &index); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, doc := range index.Documents {
		for _, occ := range doc.Occurrences {
			if occ.Symbol == "" && len(occ.Diagnostics) > 0 && occ.Diagnostics[0].Code == "conditional-compilation" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("missing symbol-free conditional diagnostic")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, "lint", out).CombinedOutput()
	if err != nil && !bytes.Contains(output, []byte("found empty symbol")) {
		t.Fatalf("unexpected scip lint failure: %v\n%s", err, output)
	}
	if err != nil {
		t.Logf("known scip lint limitation: %s", bytes.TrimSpace(output))
	}
}

// is_reference links are permitted between an external synonym and a symbol
// defined in this index. Older lint versions only search external symbols.
func TestSCIPLintExternalRelationshipCompatibility(t *testing.T) {
	executable := findSCIP(t)
	root := t.TempDir()
	source := "CREATE TABLE hr.emp (id NUMBER);\n"
	catalog := `{"objects":[{"schema":"PUBLIC","name":"EMP_ALIAS","kind":"synonym","target":{"schema":"HR","name":"EMP"}}]}`
	for name, data := range map[string]string{"a.sql": source, "catalog.json": catalog} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "index.scip")
	if err := Index(Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR", Catalog: "catalog.json"}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var index scip.Index
	if err := proto.Unmarshal(encoded, &index); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, info := range index.ExternalSymbols {
		if info.Symbol == "scip-plsql oracle demo 1 PUBLIC/EMP_ALIAS#" {
			for _, rel := range info.Relationships {
				if rel.IsReference && rel.Symbol == "scip-plsql oracle demo 1 HR/EMP#" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("missing external synonym relationship")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, executable, "lint", out).CombinedOutput()
	if err != nil && !bytes.Contains(output, []byte("couldn't find #2 in external symbols")) {
		t.Fatalf("unexpected scip lint failure: %v\n%s", err, output)
	}
	if err != nil {
		t.Logf("known scip lint limitation: %s", bytes.TrimSpace(output))
	}
}
