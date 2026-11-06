#!/usr/bin/env python3
"""Compile the pinned utPLSQL demo function and compare it with PL/Scope.

Requires an already-running Oracle Free container, a pre-created schema and
ORACLE_DSN for that schema. The database is modified by creating BETWNSTR.
"""
import argparse
import os
import pathlib
import re
import subprocess
import tempfile
import urllib.parse


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", required=True)
    parser.add_argument("--corpus", type=pathlib.Path, required=True, help="materialized N1 root")
    parser.add_argument("--binary", type=pathlib.Path, required=True, help="indexer built with -tags integration")
    parser.add_argument("--compare-bin", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    dsn = os.environ.get("ORACLE_DSN", "")
    uri = urllib.parse.urlsplit(dsn)
    if uri.scheme != "oracle" or not uri.username or not uri.password or not uri.path.strip("/"):
        parser.error("ORACLE_DSN must be oracle://user:password@host:port/service")
    user, password = urllib.parse.unquote(uri.username), urllib.parse.unquote(uri.password)
    if not re.fullmatch(r"[A-Za-z][A-Za-z0-9_$#]*", user) or not re.fullmatch(r"[A-Za-z0-9_$#]+", password) or not re.fullmatch(r"[A-Za-z0-9._-]+", uri.path.strip("/")):
        parser.error("demo requires simple SQL*Plus-safe username, password and service")
    original = args.corpus / "utPLSQL-demo-project/source/between_string/R__ut3_demo.betwnstr.fnc"
    text = original.read_text()
    prefix = re.match(r"(?i)^\s*create\s+or\s+replace\s+", text)
    if not prefix:
        raise ValueError("unexpected function header; refusing to compare wrong offsets")
    sql = (f"connect {user}/{password}@localhost/{uri.path.strip('/')}\n"
           "whenever sqlerror exit sql.sqlcode\n"
           "alter session set PLSCOPE_SETTINGS='IDENTIFIERS:ALL, STATEMENTS:ALL';\n" + text +
           "\ndeclare n number; begin select count(*) into n from user_objects where object_name='BETWNSTR' and status='VALID'; "
           "if n != 1 then raise_application_error(-20000,'BETWNSTR failed to compile'); end if; end;\n/\nexit\n")
    subprocess.run(["docker", "exec", "-i", args.container, "sqlplus", "-s", "/nolog"], input=sql, text=True, check=True, stdout=subprocess.DEVNULL)
    args.output.mkdir(parents=True, exist_ok=True)
    output, binary, comparer = args.output.resolve(), args.binary.resolve(strict=True), args.compare_bin.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="plscope-fixture-") as tmp:
        source = pathlib.Path(tmp) / "betwnstr.fnc"
        source.write_text(text)
        index = output / "index.scip"
        subprocess.run([str(binary), "index", "--schema", user.upper(), "--database", "oracle-free", "--package-version", "1", "--output", str(index), tmp], check=True)
        rows = output / "identifiers.json"
        subprocess.run([str(binary), "extract", "--schema", user.upper(), "--object", "BETWNSTR", "--kind", "identifiers", "--output", str(rows)], check=True)
        with (output / "comparison.json").open("w") as report:
            subprocess.run([str(comparer), "--index", str(index), "--plscope", str(rows), "--source", str(source), "--document", source.name, "--first-line-prefix", str(len(prefix.group()))], stdout=report, check=True)
    print(output / "comparison.json")


if __name__ == "__main__":
    main()
