package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"github.com/jjrodrig/scip-plsql/internal/indexer"
	"os"
	"runtime/pprof"
	"strings"
	"time"
)

// oracleIntegrationEnabled defaults to false so main.go can also be built as
// a standalone file. The integration-tagged driver enables it during init.
var oracleIntegrationEnabled bool

type ccFlag map[string]string

func (f ccFlag) String() string { return "" }
func (f ccFlag) Set(value string) error {
	key, val, ok := strings.Cut(value, "=")
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	if !ok || key == "" || val == "" {
		return fmt.Errorf("se esperaba NOMBRE=VALOR")
	}
	f[key] = val
	return nil
}
func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "help", "-h", "--help":
		usage()
	case "index":
		err = runIndex(os.Args[2:])
	case "export-catalog":
		err = runOracleExport(os.Args[1:])
	case "extract":
		err = runOracleExport(os.Args[1:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: scip-plsql <command> [options]

Commands:
  index [directory]       Index PL/SQL source files and write an SCIP index.
  export-catalog          Export an Oracle data dictionary snapshot as JSON.
  extract                 Export PL/Scope identifiers and statements as JSON.
  help                    Show this help message.

Use "scip-plsql <command> -h" to see command-specific options.`)
}

func runIndex(args []string) error {
	f := flag.NewFlagSet("index", flag.ExitOnError)
	f.Usage = func() {
		fmt.Fprintln(f.Output(), "Usage: scip-plsql index [options] [directory]")
		fmt.Fprintln(f.Output(), "\nOptions:")
		f.PrintDefaults()
	}
	output := f.String("output", "index.scip", "output SCIP index file")
	db := f.String("database", "database", "logical database name")
	version := f.String("package-version", "0.0.0", "logical database version")
	schema := f.String("schema", "PUBLIC", "default schema")
	schemaPath := f.String("schema-path", "", "regular expression with a capture group for the schema in each path")
	catalog := f.String("catalog", "", "Oracle data dictionary snapshot in JSON format")
	maxTokens := f.Int("max-tokens", 1000000, "maximum number of tokens per compilation unit")
	jobs := f.Int("jobs", 0, "parser workers (0 uses GOMAXPROCS)")
	cache := f.String("cache-dir", "", "optional directory for the SCIP document cache")
	cacheStats := f.Bool("cache-stats", false, "print cache statistics")
	showMetrics := f.Bool("metrics", false, "print parsing, resolution, and memory metrics")
	cpuProfile := f.String("cpu-profile", "", "write a CPU profile to this file")
	legacyEncoding := f.String("legacy-encoding", "auto", "encoding for non-UTF-8 files: auto, windows-1252, or iso-8859-15")
	var includes, excludes repeatedString
	f.Var(&includes, "include", "source path glob to include; repeatable, supports **")
	f.Var(&excludes, "exclude", "source path glob to exclude; repeatable, supports **")
	flags := ccFlag{}
	f.Var(flags, "ccflag", "conditional compilation flag NAME=VALUE; repeatable")
	_ = f.Parse(args)
	if f.NArg() > 1 {
		return fmt.Errorf("expected at most one directory")
	}
	if *maxTokens < 1 {
		return fmt.Errorf("--max-tokens must be positive")
	}
	root := "."
	if f.NArg() == 1 {
		root = f.Arg(0)
	}
	var stats indexer.CacheStats
	var metrics indexer.Metrics
	options := indexer.Options{Root: root, Output: *output, Database: *db, Version: *version, Schema: *schema, SchemaPath: *schemaPath, CCFlags: flags, Catalog: *catalog, MaxTokens: *maxTokens, Workers: *jobs, CacheDir: *cache, CacheStats: &stats, LegacyEncoding: *legacyEncoding, IncludeGlobs: includes, ExcludeGlobs: excludes}
	if *showMetrics {
		options.Metrics = &metrics
	}
	if *cpuProfile != "" {
		profile, err := os.Create(*cpuProfile)
		if err != nil {
			return fmt.Errorf("create CPU profile: %w", err)
		}
		if err := pprof.StartCPUProfile(profile); err != nil {
			profile.Close()
			return err
		}
		defer func() { pprof.StopCPUProfile(); profile.Close() }()
	}
	if err := indexer.Index(options); err != nil {
		return err
	}
	if *cacheStats {
		fmt.Fprintf(os.Stderr, "Cache statistics:\n  Hits:       %d\n  Misses:     %d\n  Invalid:    %d\n  Writes:     %d\n", stats.Hits, stats.Misses, stats.Invalid, stats.Writes)
	}
	if *showMetrics {
		locPerSecond := 0.0
		if metrics.Elapsed > 0 {
			locPerSecond = float64(metrics.Lines) / metrics.Elapsed.Seconds()
		}
		fmt.Fprintf(os.Stderr, "Indexing metrics:\n  Files:                   %d\n  Lines of code:           %d\n  Compilation units:       %d\n  Parsed units:            %d\n  Degraded units:          %d\n  Resolved references:     %d\n  Parse time:              %s\n  Total time:              %s\n  Throughput:              %.0f LOC/s\n  Peak sampled heap:       %s\n", metrics.Files, metrics.Lines, metrics.Units, metrics.ParsedUnits, metrics.DegradedUnits, metrics.ResolvedReferences, metrics.ParseDuration.Round(time.Millisecond), metrics.Elapsed.Round(time.Millisecond), locPerSecond, formatBytes(metrics.PeakHeapBytes))
	}
	return nil
}

func runOracleExport(args []string) error {
	name := args[0]
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.Usage = func() {
		fmt.Fprintf(f.Output(), "Usage: scip-plsql %s [options]\n\nOptions:\n", name)
		f.PrintDefaults()
	}
	defaultDSN := os.Getenv("ORACLE_DSN")
	dsn := f.String("dsn", defaultDSN, "Oracle DSN (defaults to ORACLE_DSN)")
	object := f.String("object", "", "PL/Scope object name (optional)")
	kind := f.String("kind", "identifiers", "PL/Scope data to export: identifiers, statements, or both")
	output := f.String("output", "-", "output JSON file, or - for stdout")
	var schemas repeatedString
	f.Var(&schemas, "schema", "schema to export; repeatable")
	if err := f.Parse(args[1:]); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	if !oracleIntegrationEnabled {
		return fmt.Errorf("this command requires building with -tags integration (includes go-ora)")
	}
	if *dsn == "" {
		return fmt.Errorf("provide --dsn or set ORACLE_DSN")
	}
	if name == "extract" && len(schemas) == 0 {
		return fmt.Errorf("extract requires --schema")
	}
	if name == "extract" && len(schemas) > 1 {
		return fmt.Errorf("extract accepts exactly one --schema")
	}
	db, err := sql.Open("oracle", *dsn)
	if err != nil {
		return fmt.Errorf("abrir Oracle: %w", err)
	}
	defer db.Close()
	ctx := context.Background()
	var writer *os.File
	var closeWriter bool
	if *output == "-" {
		writer = os.Stdout
	} else {
		writer, err = os.Create(*output)
		if err != nil {
			return fmt.Errorf("crear salida: %w", err)
		}
		closeWriter = true
	}
	if closeWriter {
		defer writer.Close()
	}
	if name == "export-catalog" {
		snapshot, err := indexer.ExportCatalog(ctx, db, schemas...)
		if err != nil {
			return err
		}
		return indexer.WriteCatalogJSON(writer, snapshot)
	}
	switch *kind {
	case "identifiers":
		identifiers, err := indexer.ExportPLScope(ctx, db, schemas[0], *object)
		if err != nil {
			return err
		}
		return indexer.WritePLScopeJSON(writer, identifiers)
	case "statements":
		statements, err := indexer.ExportPLScopeStatements(ctx, db, schemas[0], *object)
		if err != nil {
			return err
		}
		return indexer.WritePLScopeStatementsJSON(writer, statements)
	case "both":
		identifiers, err := indexer.ExportPLScope(ctx, db, schemas[0], *object)
		if err != nil {
			return err
		}
		statements, err := indexer.ExportPLScopeStatements(ctx, db, schemas[0], *object)
		if err != nil {
			return err
		}
		return indexer.WritePLScopeSnapshotJSON(writer, indexer.PLScopeSnapshot{Identifiers: identifiers, Statements: statements})
	default:
		return fmt.Errorf("--kind must be identifiers, statements, or both")
	}
}

func formatBytes(value uint64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	amount := float64(value)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, suffix := range units {
		amount /= unit
		if amount < unit || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", amount, suffix)
		}
	}
	return fmt.Sprintf("%d B", value)
}

type repeatedString []string

func (s *repeatedString) String() string { return strings.Join(*s, ",") }
func (s *repeatedString) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("el esquema no puede estar vacío")
	}
	*s = append(*s, value)
	return nil
}
