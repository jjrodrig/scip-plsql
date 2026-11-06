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

func TestCatalogIndexTracksEveryMatchingIdentifier(t *testing.T) {
	for _, key := range []string{"HR.PKG.MEMBER", `HR."Mixed.Name".FIELD`, `"Sales".ORDERS.ID`} {
		s := &state{objects: map[string]string{key: "one"}}
		before := s.buildCacheCatalog()
		s.objects[key] = "two"
		after := s.buildCacheCatalog()
		parts := strings.Split(key, ".")
		for i := range parts {
			for j := i + 1; j <= len(parts); j++ {
				part := strings.Join(parts[i:j], ".")
				for _, candidate := range []string{part, name(part), strings.ToUpper(part)} {
					if keyUsesIdentifier(key, map[string]bool{candidate: true}) && before[candidate] == after[candidate] {
						t.Errorf("catalog change in %q ignored for %q", key, candidate)
					}
				}
			}
		}
	}
}

func TestCacheInvalidatesForNamesJoinedInDynamicSQL(t *testing.T) {
	for _, expression := range []string{"'SELECT id FROM em-\np'", "'SELECT id FROM em' || 'p'"} {
		t.Run(expression, func(t *testing.T) {
			root := t.TempDir()
			sql := "CREATE PROCEDURE hr.p IS BEGIN EXECUTE IMMEDIATE " + expression + "; END;\n/\n"
			if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte(sql), 0600); err != nil {
				t.Fatal(err)
			}
			var stats CacheStats
			opts := Options{Root: root, Output: filepath.Join(t.TempDir(), "index.scip"), Database: "demo", Version: "1", Schema: "HR", CacheDir: filepath.Join(t.TempDir(), "cache"), CacheStats: &stats}
			if err := Index(opts); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "b.sql"), []byte("CREATE TABLE hr.emp (id NUMBER);\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := Index(opts); err != nil {
				t.Fatal(err)
			}
			if stats.Hits != 0 || stats.Misses != 2 {
				t.Fatalf("new object must invalidate dynamic SQL: %+v", stats)
			}
			cached, err := os.ReadFile(opts.Output)
			if err != nil {
				t.Fatal(err)
			}
			opts.CacheDir = ""
			if err := Index(opts); err != nil {
				t.Fatal(err)
			}
			fresh, err := os.ReadFile(opts.Output)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(cached, fresh) {
				t.Fatal("cached output differs after new dynamic SQL target")
			}
		})
	}
}

func TestCachePreservesRemoteSymbolsAcrossDocuments(t *testing.T) {
	root := t.TempDir()
	for filename, text := range map[string]string{
		"a.sql": "SELECT id FROM HR.EMP@remote_db;\nBEGIN HR.PKG.PROC@remote_db; END;\n",
		"b.sql": "SELECT id FROM hr.emp@remote_db;\nBEGIN hr.pkg.proc@remote_db; END;\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filename), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stats CacheStats
	opts := Options{Root: root, Output: filepath.Join(t.TempDir(), "out.scip"), Database: "demo", Version: "1", Schema: "HR", CacheDir: filepath.Join(t.TempDir(), "cache"), CacheStats: &stats}
	run := func() []byte {
		t.Helper()
		if err := Index(opts); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(opts.Output)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	cold := run()
	var index scip.Index
	if err := proto.Unmarshal(cold, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.ExternalSymbols) < 3 {
		t.Fatalf("expected remote symbols, got %d", len(index.ExternalSymbols))
	}
	warm := run()
	if !bytes.Equal(cold, warm) || stats.Hits != 2 || stats.Misses != 0 {
		t.Fatalf("warm cache: %+v equal=%v", stats, bytes.Equal(cold, warm))
	}
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte("SELECT 1 FROM dual;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	mixed := run()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("mixed cache: %+v", stats)
	}
	opts.CacheDir = ""
	fresh := run()
	if !bytes.Equal(mixed, fresh) {
		t.Fatal("mixed cached and fresh documents lost external symbols")
	}
	opts.CacheDir = filepath.Join(t.TempDir(), "other-cache")
	run()
	opts.Version = "2"
	changedVersion := run()
	if stats.Misses != 2 {
		t.Fatalf("package version change must invalidate remote symbols: %+v", stats)
	}
	opts.CacheDir = ""
	if !bytes.Equal(changedVersion, run()) {
		t.Fatal("package version change reused stale symbols")
	}
}

func TestDocumentCacheInvalidationAndCorruption(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	files := map[string]string{
		"spec.sql":   "CREATE PACKAGE hr.pkg AS PROCEDURE run(p NUMBER); END;\n/\n",
		"body.sql":   "CREATE PACKAGE BODY hr.pkg AS PROCEDURE run(p NUMBER) IS BEGIN NULL; END; END;\n/\n",
		"caller.sql": "CREATE PROCEDURE hr.client IS BEGIN pkg.run(1); END;\n/\n",
	}
	for file, text := range files {
		if err := os.WriteFile(filepath.Join(root, file), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stats CacheStats
	out := filepath.Join(t.TempDir(), "index.scip")
	opts := Options{Root: root, Output: out, Database: "demo", Version: "1", Schema: "HR", CacheDir: cache, CacheStats: &stats}
	run := func() []byte {
		t.Helper()
		if err := Index(opts); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first := run()
	if stats.Hits != 0 || stats.Misses != 3 || stats.Writes != 3 {
		t.Fatalf("cold cache: %+v", stats)
	}
	second := run()
	if !bytes.Equal(first, second) || stats.Hits != 3 || stats.Misses != 0 {
		t.Fatalf("warm cache: %+v equal=%v", stats, bytes.Equal(first, second))
	}
	body := "CREATE PACKAGE BODY hr.pkg AS PROCEDURE run(p NUMBER) IS BEGIN NULL; NULL; END; END;\n/\n"
	if err := os.WriteFile(filepath.Join(root, "body.sql"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	changed := run()
	if stats.Hits != 2 || stats.Misses != 1 {
		t.Fatalf("body-only invalidation: %+v", stats)
	}
	uncached := opts
	uncached.CacheDir = ""
	uncached.CacheStats = nil
	uncached.Output = filepath.Join(t.TempDir(), "uncached.scip")
	if err := Index(uncached); err != nil {
		t.Fatal(err)
	}
	baseline, err := os.ReadFile(uncached.Output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(changed, baseline) {
		t.Fatal("cached output differs from fresh output")
	}
	spec := "CREATE PACKAGE hr.pkg AS PROCEDURE run(p VARCHAR2); END;\n/\n"
	if err := os.WriteFile(filepath.Join(root, "spec.sql"), []byte(spec), 0600); err != nil {
		t.Fatal(err)
	}
	run()
	if stats.Hits != 0 || stats.Misses != 3 {
		t.Fatalf("signature change must invalidate dependents: %+v", stats)
	}
	err = filepath.WalkDir(cache, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".pb" {
			return os.WriteFile(path, []byte("corrupt"), 0600)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after := run()
	if stats.Invalid != 3 || stats.Misses != 3 || stats.Writes != 3 {
		t.Fatalf("corrupt entries: %+v", stats)
	}
	fresh := uncached
	fresh.Output = filepath.Join(t.TempDir(), "fresh.scip")
	if err := Index(fresh); err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(fresh.Output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, expected) {
		t.Fatal("corrupt cache changed output")
	}
}
func TestCacheInvalidatesForNewNameAndConditionalFlags(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	source := `$IF $$feature $THEN
CREATE TABLE hr.enabled (id NUMBER);
$ELSE
CREATE TABLE hr.disabled (id NUMBER);
$END
`
	if err := os.WriteFile(filepath.Join(root, "schema.sql"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "query.sql"), []byte("SELECT id FROM enabled;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stats CacheStats
	opts := Options{Root: root, Output: filepath.Join(t.TempDir(), "index.scip"), Database: "demo", Version: "1", Schema: "HR", CacheDir: cache, CacheStats: &stats, CCFlags: map[string]string{"feature": "false"}}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	opts.CCFlags["feature"] = "true"
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	if stats.Hits != 0 || stats.Misses != 2 {
		t.Fatalf("flag change must invalidate documents: %+v", stats)
	}
	before, err := os.ReadFile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	opts.CacheDir = ""
	opts.CacheStats = nil
	opts.Output = filepath.Join(t.TempDir(), "uncached.scip")
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("flag change reused stale output")
	}
}
func TestCacheDirectoryCannotBeRoot(t *testing.T) {
	root := t.TempDir()
	err := Index(Options{Root: root, Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", CacheDir: "."})
	if err == nil {
		t.Fatal("accepted project root as cache directory")
	}
}

func TestCacheDirectoryInsideProjectIsExcluded(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte("CREATE TABLE hr.emp (id NUMBER);\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var stats CacheStats
	opts := Options{Root: root, Output: filepath.Join(t.TempDir(), "index.scip"), Database: "demo", Version: "1", Schema: "HR", CacheDir: "cache", CacheStats: &stats}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cache", "stray.sql"), []byte("CREATE TABLE hr.fake (id NUMBER);"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Index(opts); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(opts.Output)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Hits != 1 || !bytes.Equal(first, second) {
		t.Fatalf("cache contents indexed or not reused: %+v", stats)
	}
}

func TestConcurrentIndexesShareCache(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(filepath.Join(root, "a.sql"), []byte("CREATE TABLE hr.emp (id NUMBER);\nSELECT id FROM hr.emp;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	outputs := make([]string, 4)
	errors := make(chan error, len(outputs))
	for i := range outputs {
		outputs[i] = filepath.Join(t.TempDir(), "index.scip")
		go func(path string) {
			errors <- Index(Options{Root: root, Output: path, Database: "demo", Version: "1", Schema: "HR", CacheDir: cache, Workers: 2})
		}(outputs[i])
	}
	for range outputs {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	first, err := os.ReadFile(outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range outputs[1:] {
		next, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, next) {
			t.Fatal("concurrent cache output differs")
		}
	}
}
