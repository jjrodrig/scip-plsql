# scip-plsql

`scip-plsql` indexes Oracle PL/SQL source files and writes a [SCIP](https://github.com/sourcegraph/scip) index for code navigation and analysis. It is an early-stage project: supported syntax is useful for real repositories, but the design's coverage and performance targets are not fully verified.

## License

This project is licensed under the GNU Affero General Public License, version 3.0
only (AGPL-3.0-only). See [LICENSE](LICENSE) for the full text. Third-party
dependencies retain their own licenses.

## Requirements

- Go 1.27.0 or later
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
- Preserves source offsets while preprocessing substitution variables and conditional compilation flags. Substitution variables currently have no configurable values: active placeholders produce a `substitution-unresolved` diagnostic, and syntax depending on their value may remain degraded.
- Handles SQL*Plus trailing-hyphen continuations, including continued literal SQL passed to `EXECUTE IMMEDIATE`, and substitution-variable dot delimiters. As in SQL*Plus, a hyphen at the end of a line is treated as continuation rather than subtraction; move a subtraction operator to the next line. Static SQL literal values are not evaluated.
- Parses a conservative subset of Oracle mining expressions with `USING *`, simple column or literal lists, `AS` aliases, inline anomaly and `COST MODEL`, preserving surrounding references. Mining model names themselves are not resolved and produce a diagnostic; other mining forms may still degrade.
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

CI installs the official SCIP CLI at `v0.5.2` and runs the acceptance tests,
the race detector and the integration-tagged build. The integration build only
checks compilation and tests; it does not connect to an Oracle server.

## Reproducible corpus measurements

`corpus/manifest.json` pins ten N1 sources by commit. Materialize them from
GitHub, or use local checkouts at those exact commits (only tracked files from
each commit are included):

```sh
python3 tools/corpus.py n1 /tmp/plsql-n1
# Offline alternative: one checkout per manifest name under the directory:
python3 tools/corpus.py n1 /tmp/plsql-n1 --local-root /path/to/checkouts
# Select one source and use a specific local checkout:
python3 tools/corpus.py n1 /tmp/plsql-n1-single --source oracle-db-examples \
  --local /path/to/oracle-db-examples
go build -o /tmp/scip-plsql ./cmd/scip-plsql
python3 tools/measure.py --binary /tmp/scip-plsql --corpus /tmp/plsql-n1 \
  --output /tmp/plsql-n1-results --label N1 \
  --revision "manifest-sha256:$(sha256sum corpus/manifest.json | cut -d' ' -f1)"
```

To avoid mixing independent schemas, measure each N1 repository separately and
group its SCIP diagnostics by code and message:

```sh
go build -o /tmp/scip-diagnostics ./cmd/scip-diagnostics
python3 tools/measure_n1.py --binary /tmp/scip-plsql \
  --diagnostics-bin /tmp/scip-diagnostics --corpus /tmp/plsql-n1 \
  --output /tmp/plsql-n1-results-by-repo --jobs 4
```

Each repository gets a `report.json` and `diagnostics.json` with counts and up
to three source locations per message. Diagnostics are occurrences, not a
count of degraded units: for example ambiguity and dynamic SQL warnings can
be emitted by units that parsed successfully. The scheduled job uses this
per-repository mode. Files may still define multiple database schemas.

The eleventh proposed source, alexandria-plsql-utils, remains excluded pending
license clarification. A combined N1 run shares one catalog across projects;
it measures robustness and throughput but is not an accuracy measurement for
the separate projects. The synthetic generator defaults to 200,000 procedures and exactly one
million physical lines; smaller runs can use `--units` and `--units-per-file`.
It is a simple throughput workload, not representative of all N3 features such
as large packages, encodings or cross-schema dependencies.

```sh
python3 tools/corpus.py n3 /tmp/plsql-n3
python3 tools/measure.py --binary /tmp/scip-plsql --corpus /tmp/plsql-n3 \
  --output /tmp/plsql-n3-results --label N3 \
  --revision '200000 units, 1000 units/file, generator v1' \
  --change workload_00000.sql
```

Measurements use a fresh cache, record cold and warm runs, compare index hashes,
and optionally measure an added statement in one synthetic file; the original
file is restored afterward. Results and raw logs are saved outside the corpus.
The indexer's reported peak is sampled Go heap, not process RSS; its line count
includes a final empty line per file ending in a newline. A changed-file run is
a small controlled edit, not a representative PR. Use `--jobs 16` on a dedicated
16-core machine to assess the design target. Timing varies with hardware and
load; no performance gate is set until a stable baseline exists. The scheduled
workflow uploads the reports even when a run fails. It does not yet compare
against a historical baseline or run `benchstat`.

Initial local results are recorded in `corpus/baselines/`. The original N1
baseline exposed a cache issue with database-link symbols; those historical
results predate its correction. The measurement fails if cold and warm index
hashes differ. The expanded local N1 baseline after the fix reused all 2,159
documents and produced byte-identical cold and warm indexes. The N3 million
line run timed out after 150 seconds with four workers on a shared machine. This
does not establish the result on the dedicated 16-core machine in the design;
other compilation and test processes ran during that measurement.
In the later isolated N3 run, the cold index completed in 149.7 seconds and
the warm run exceeded the 150-second limit; both used four parser workers.
After precalculating catalog fingerprints, a repeat with the same synthetic
workload and worker count took 5.5 seconds cold, 4.9 seconds warm, and 4.8
seconds after a one-file edit. It is a simple generated workload; these times
do not establish the design target for real repositories.

The `--cpu-profile` index option saves a Go CPU profile. For example, run a
source twice with the same cache directory, setting a different profile path
each time, then inspect the warm profile with `go tool pprof -top -cum`. The
initial isolated N1 profile and per-repository diagnostic baselines are saved
in `corpus/baselines/`.

### PL/Scope reference smoke test

The local PL/Scope comparison uses the pinned `BETWNSTR`, `AWARD_BONUS` and
`REMOVE_ROOMS_BY_NAME` objects from `utPLSQL-demo-project`. Start a disposable
Oracle Free container with a clean test user, build the integration-tagged
indexer and comparison tool, and point `ORACLE_DSN` at the test user. The script
creates the three objects and their test tables in that schema, enables
PL/Scope, indexes the original sources and writes a report for each object:

```sh
docker run -d --rm --name oracle-test -p 127.0.0.1:1521:1521 \
  -e ORACLE_PASSWORD=TestPass123 -e APP_USER=SCIP_TEST \
  -e APP_USER_PASSWORD=TestPass123 gvenzl/oracle-free:23-slim-faststart
# Wait until: docker exec oracle-test healthcheck.sh
go build -tags integration -o /tmp/scip-plsql-oracle ./cmd/scip-plsql
go build -o /tmp/scip-plscope-compare ./cmd/scip-plscope-compare
ORACLE_DSN='oracle://SCIP_TEST:TestPass123@127.0.0.1:1521/FREEPDB1' \
  python3 tools/plscope_demo.py --container oracle-test --corpus /tmp/plsql-n1 \
  --binary /tmp/scip-plsql-oracle --compare-bin /tmp/scip-plscope-compare \
  --output /tmp/plscope-results
docker stop oracle-test
```

The comparator checks whether a PL/Scope signature's usages at matching source
positions point to the same SCIP symbol as its declaration. It reports built-in
or otherwise undeclared signatures as `unanchored`; it does not establish the
correctness of the declaration's target, compare access roles, or claim corpus
accuracy. It accounts for Oracle removing `CREATE OR REPLACE` from the first
stored source line. The three-object result is in
`corpus/baselines/plscope-expanded-local-2026-11-06.json`.

### N2: packages, SQL and external catalog

The separate `corpus/n2/fixtures` scenario creates packages in two schemas,
overloads, a synonym, `%TYPE` and `%ROWTYPE` anchors, an object type and a
trigger with `:NEW` and `:OLD`. It also creates a table that exists only in
Oracle and the catalog, not the indexed source tree. Run it only on a
disposable database: it creates `N2_EXT` and requires the temporary SYSTEM
password through `N2_ADMIN_PASSWORD`.
`scip-n2-compare` joins PL/Scope signatures across documents. Explicit fixture
anchors check the expected package, both overloads and table/column symbols;
other declarations are used as consistency anchors. Oracle reports both sides
of an SQL column assignment as `REFERENCE`, so those read/write expectations
and trigger row-variable roles are specified explicitly in the fixture rather
than inferred from PL/Scope. The comparator reports multiple overload candidates
as ambiguous rather than counting them as exact matches. Some built-ins remain
unanchored.

With a running disposable Oracle Free container and a **clean** schema, run:

```sh
go build -tags integration -o /tmp/scip-plsql-oracle ./cmd/scip-plsql
go build -o /tmp/scip-n2-compare ./cmd/scip-n2-compare
ORACLE_DSN='oracle://SCIP_TEST:TestPass123@127.0.0.1:1521/FREEPDB1' \
  N2_ADMIN_PASSWORD=TestPass123 \
  python3 tools/run_n2.py --container oracle-test --binary /tmp/scip-plsql-oracle \
  --compare-bin /tmp/scip-n2-compare --output /tmp/n2-results --check
```

`--check` enforces the current small-fixture baseline and validates the explicit
anchors and roles; it is not a general accuracy threshold. The weekly/manual
`.github/workflows/n2.yml` job runs this scenario and the pinned utPLSQL demo,
then uploads reports. The first local result is
`corpus/baselines/n2-fixture-local-2026-11-06.json`.

## Current limitations

- Resolution is best-effort; unresolved or unsupported syntax can produce diagnostics or incomplete references. An absent reference does not prove that the name is undefined.
- Non-literal dynamic SQL and runtime-dependent SQL strings are not resolved.
- The parser and resolver do not yet cover the full Oracle grammar or all PL/SQL name-resolution and overload rules.
- The optional cache stores SCIP documents, but declarations and source units are still rebuilt on each run.
- Source files, ASTs, and documents are not yet processed through a globally bounded streaming pipeline. Existing benchmarks and the small PL/Scope sample do not establish the design's accuracy, memory, and throughput targets for real workloads.
- Liquibase changelog ordering is not implemented; Flyway versioned migrations are applied in numeric order for supported table alterations.
- Some SCIP CLI versions reject valid SCIP constructs such as diagnostic-only occurrences or external-symbol relationships whose target is defined in the repository. Compatibility tests account for these known linter limitations.

