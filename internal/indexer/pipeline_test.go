package indexer

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func TestParallelParsingDeterministicAcrossWorkerCounts(t *testing.T) {
	root := t.TempDir()
	for f := 0; f < 12; f++ {
		var sql strings.Builder
		fmt.Fprintln(&sql, "ALTER SESSION SET CURRENT_SCHEMA = hr;")
		for n := 0; n < 12; n++ {
			fmt.Fprintf(&sql, "CREATE TABLE table_%d_%d (id NUMBER, val NUMBER);\n", f, n)
			fmt.Fprintf(&sql, "CREATE PROCEDURE proc_%d_%d IS BEGIN UPDATE table_%d_%d SET val=val+1 WHERE id=1; END;\n/\n", f, n, f, n)
		}
		fmt.Fprintf(&sql, "CREATE PACKAGE BODY wrapped_%d wrapped\nabc\n/\n", f)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%02d.sql", f)), []byte(sql.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var reference []byte
	for _, workers := range []int{1, 2, 8, 8} {
		path := filepath.Join(t.TempDir(), "index.scip")
		if err := Index(Options{Root: root, Output: path, Database: "demo", Version: "1", Schema: "APP", Workers: workers}); err != nil {
			t.Fatalf("workers %d: %v", workers, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if reference == nil {
			var idx scip.Index
			if err := proto.Unmarshal(data, &idx); err != nil {
				t.Fatal(err)
			}
			if len(idx.Documents) != 12 {
				t.Fatalf("got %d documents", len(idx.Documents))
			}
			count := 0
			for _, doc := range idx.Documents {
				count += len(doc.Occurrences)
			}
			if count < 1000 {
				t.Fatalf("unexpectedly few occurrences: %d", count)
			}
			reference = data
		} else if !bytes.Equal(reference, data) {
			t.Fatalf("output differs with %d workers", workers)
		}
	}
}
func TestNegativeWorkerCountRejected(t *testing.T) {
	err := Index(Options{Root: t.TempDir(), Output: "index.scip", Database: "demo", Version: "1", Schema: "HR", Workers: -1})
	if err == nil {
		t.Fatal("negative worker count accepted")
	}
}

func BenchmarkIndexParallel(b *testing.B) {
	root := b.TempDir()
	var sql strings.Builder
	for n := 0; n < 250; n++ {
		fmt.Fprintf(&sql, "CREATE PROCEDURE hr.p_%d IS BEGIN NULL; END;\n/\n", n)
	}
	for file := 0; file < 4; file++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%d.sql", file)), []byte(sql.String()), 0600); err != nil {
			b.Fatal(err)
		}
	}
	for _, workers := range []int{1, 4} {
		b.Run(fmt.Sprintf("workers_%d", workers), func(b *testing.B) {
			opts := Options{Root: root, Output: filepath.Join(b.TempDir(), "index.scip"), Database: "demo", Version: "1", Schema: "HR", Workers: workers}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := Index(opts); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
