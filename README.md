# scip-plsql

`scip-plsql` indexes Oracle PL/SQL source files and writes a [SCIP](https://github.com/sourcegraph/scip) index for code navigation and analysis. It is an early-stage project: supported syntax is useful for real repositories, but coverage and performance targets from the design are not yet complete or measured.

## Requirements

- Go 1.26.2
- The Oracle driver is only needed for the optional `export-catalog` and `extract` commands.
- The official `scip` CLI is optional and is used by some acceptance tests.

## Build and run

Build the offline indexer:

```sh
go build -o scip-plsql ./cmd/scip-plsql
```

See available commands and flags:

```sh
./scip-plsql help
./scip-plsql index --help
```

Index a source tree:

```sh
./scip-plsql index \
  --database erp-core \
  --package-version 1.0 \
  --schema HR \
  --output index.scip \
  ./path/to/source
```

The output defaults to `index.scip` in the current directory. The parser uses `GOMAXPROCS` workers by default; set `--jobs` to choose a worker count.

## Indexing features

- Indexes packages, standalone procedures and functions, object types, triggers, tables, views, sequences, and synonyms.
- Resolves declarations and references across source files, including package members, local scopes, parameters, table columns, aliases, CTEs, subqueries, and supported overloads.
- Handles common embedded SQL statements (`SELECT`, `INSERT`, `UPDATE`, `DELETE`, and `MERGE`) and literal dynamic SQL.
- Loads an optional data dictionary snapshot with `--catalog` to provide external symbols for objects that are not present in the repository.
- Represents database-link references as external SCIP symbols in a package specific to each link.
- Preserves source offsets while preprocessing substitution variables and conditional compilation flags.
- Reports parser and degraded-unit diagnostics in the SCIP index instead of stopping on the first malformed unit.
- Supports source filters, schema selection, optional document caching, and parsing and memory metrics.

Source files with common PL/SQL extensions are included by default. Repeat `--include` and `--exclude` to filter paths; globs support `**`:

```sh
./scip-plsql index \
  --include '**/*.sql' \
  --exclude 'migrations/archive/**' \
  --schema-path '^db/([^/]+)/' \
  --ccflag debug=true \
  ./path/to/source
```

`--schema` sets the default schema. `--schema-path` can derive it from a path using a capture group. An explicit schema in a `CREATE` statement takes precedence; `ALTER SESSION SET CURRENT_SCHEMA` affects subsequent units in the script. Repeat `--ccflag NAME=VALUE` to select conditional-compilation branches. `--max-tokens` sets the per-unit token limit.

Use `--metrics` to print parsing, resolution, throughput, and sampled heap usage. Use `--cache-dir` to enable the optional document cache and `--cache-stats` to print cache counters:

```sh
./scip-plsql index \
  --cache-dir .scip-plsql-cache \
  --cache-stats \
  --metrics \
  ./path/to/source
```

## Oracle exports

The default build does not include a database driver and does not connect to Oracle. Build with the `integration` tag to enable Oracle-backed commands:

```sh
go build -tags integration -o scip-plsql ./cmd/scip-plsql
```

Export a dictionary snapshot for use with `--catalog`:

```sh
ORACLE_DSN='oracle://user:password@host:1521/service' \
  ./scip-plsql export-catalog --schema HR --output catalog.json
```

`--dsn` can be used instead of `ORACLE_DSN`. Repeat `--schema` to export selected schemas; if omitted, all schemas visible to the connected user are exported.

Export PL/Scope data for a schema:

```sh
ORACLE_DSN='oracle://user:password@host:1521/service' \
  ./scip-plsql extract --schema HR --object EMP_PKG --kind both --output plscope.json
```

`extract` requires exactly one `--schema`. `--kind` accepts `identifiers`, `statements`, or `both`. The Oracle objects must have been compiled with PL/Scope enabled. Statement exports include `TEXT` and `FULL_TEXT` when Oracle provides them.

## Dictionary snapshot format

Pass a local JSON snapshot to the indexer with `--catalog`. The indexer does not connect to Oracle while indexing. A minimal snapshot looks like this:

```json
{
  "objects": [
    {
      "schema": "HR",
      "name": "EMPLOYEES",
      "kind": "table",
      "columns": [
        {"name": "EMPLOYEE_ID", "type": "NUMBER"}
      ]
    },
    {
      "schema": "PUBLIC",
      "name": "STAFF",
      "kind": "synonym",
      "target": {"schema": "HR", "name": "EMPLOYEES"}
    },
    {
      "schema": "SYS",
      "name": "DBMS_OUTPUT",
      "kind": "package",
      "members": [
        {
          "name": "PUT_LINE",
          "kind": "procedure",
          "parameters": [
            {"name": "A", "type": "VARCHAR2", "mode": "IN"}
          ]
        }
      ]
    }
  ]
}
```

Supported object kinds include `table`, `view`, `package`, `type`, `procedure`, `function`, `sequence`, and `synonym`. Package and type members can be procedures or functions. Function return types use `return_type`; optional parameters use `"default": true`. Synonym targets may include a `dblink` field. Repository declarations take precedence over snapshot objects, and snapshot columns fill gaps in repository table and view definitions.

## Source encoding

UTF-8, UTF-8 with BOM, UTF-16, and UTF-32 BOMs are recognized. For non-UTF-8 files without a BOM, a heuristic chooses between Windows-1252 and ISO-8859-15. If the source encoding is known, set `--legacy-encoding windows-1252` or `--legacy-encoding iso-8859-15`. Transcoded text is embedded in the SCIP document; original source files are left unchanged. Some SCIP consumers ignore embedded text and may show incorrect positions for transcoded files.

## Tests

Run the Go test suite:

```sh
go test ./...
```

If the official `scip` CLI is installed, acceptance tests also run SCIP linting, statistics, and snapshot checks. Set `SCIP_BIN` to its path if it is not on `PATH`:

```sh
SCIP_BIN=/path/to/scip go test ./...
```

When `scip` is unavailable and `SCIP_BIN` is unset, those CLI-backed checks are skipped.

## Current limitations

- Resolution is best-effort; unresolved or unsupported syntax can produce diagnostics or incomplete references. An absent reference does not prove that the name is undefined.
- Non-literal dynamic SQL and runtime-dependent SQL strings are not resolved.
- The parser and resolver do not yet cover the full Oracle grammar or all PL/SQL name-resolution and overload rules.
- The optional cache stores SCIP documents, but declarations and source units are still rebuilt on each run.
- Source files, ASTs, and documents are not yet processed through a globally bounded streaming pipeline. The design's accuracy, memory, and throughput goals have not been benchmarked.
- Liquibase changelog ordering is not implemented; Flyway versioned migrations are applied in numeric order for supported table alterations.
- Some SCIP CLI versions reject valid SCIP constructs such as diagnostic-only occurrences or external-symbol relationships whose target is defined in the repository. Compatibility tests account for these known linter limitations.

