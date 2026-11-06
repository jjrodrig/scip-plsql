#!/usr/bin/env python3
"""Compile three pinned utPLSQL demo objects and compare them with PL/Scope.

Requires an already-running Oracle Free container, a pre-created schema and
ORACLE_DSN for a clean schema. The database is modified by creating tables and
the BETWNSTR, AWARD_BONUS and REMOVE_ROOMS_BY_NAME objects.
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
    source_root = args.corpus / "utPLSQL-demo-project/source"
    fixtures = [
        ("BETWNSTR", "between_string/R__ut3_demo.betwnstr.fnc", "betwnstr.fnc"),
        ("AWARD_BONUS", "award_bonus/R__ut3_demo.award_bonus.prc", "award_bonus.prc"),
        ("REMOVE_ROOMS_BY_NAME", "remove_rooms_by_name/R__ut3_demo.remove_rooms_by_name.prc", "remove_rooms_by_name.prc"),
    ]
    tables = ["award_bonus/V1.01__employees_test.sql", "remove_rooms_by_name/V1.02__rooms.sql"]
    texts = {name: (source_root / path).read_text() for name, path, _ in fixtures}
    prefixes = {name: re.match(r"(?i)^\s*create\s+or\s+replace\s+", text) for name, text in texts.items()}
    if any(not prefix for prefix in prefixes.values()):
        raise ValueError("unexpected object header; refusing to compare wrong offsets")
    sql = (f"connect {user}/{password}@localhost/{uri.path.strip('/')}\n"
           "whenever sqlerror exit sql.sqlcode\n"
           "alter session set PLSCOPE_SETTINGS='IDENTIFIERS:ALL, STATEMENTS:ALL';\n" +
           "\n".join((source_root / path).read_text() for path in tables) + "\n" +
           "\n".join(texts[name] for name, _, _ in fixtures) +
           "\ndeclare n number; begin select count(*) into n from user_objects where object_name in "
           "('BETWNSTR','AWARD_BONUS','REMOVE_ROOMS_BY_NAME') and status='VALID'; "
           "if n != 3 then raise_application_error(-20000,'demo objects failed to compile'); end if; end;\n/\nexit\n")
    subprocess.run(["docker", "exec", "-i", args.container, "sqlplus", "-s", "/nolog"], input=sql, text=True, check=True, stdout=subprocess.DEVNULL)
    args.output.mkdir(parents=True, exist_ok=True)
    output, binary, comparer = args.output.resolve(), args.binary.resolve(strict=True), args.compare_bin.resolve(strict=True)
    with tempfile.TemporaryDirectory(prefix="plscope-fixture-") as tmp:
        for name, _, filename in fixtures:
            (pathlib.Path(tmp) / filename).write_text(texts[name])
        for path in tables:
            (pathlib.Path(tmp) / pathlib.Path(path).name).write_text((source_root / path).read_text())
        index = output / "index.scip"
        subprocess.run([str(binary), "index", "--schema", user.upper(), "--database", "oracle-free", "--package-version", "1", "--output", str(index), tmp], check=True)
        for name, _, filename in fixtures:
            rows = output / (name.lower() + "-identifiers.json")
            subprocess.run([str(binary), "extract", "--schema", user.upper(), "--object", name, "--kind", "identifiers", "--output", str(rows)], check=True)
            with (output / (name.lower() + "-comparison.json")).open("w") as report:
                subprocess.run([str(comparer), "--index", str(index), "--plscope", str(rows), "--source", str(pathlib.Path(tmp) / filename), "--document", filename, "--first-line-prefix", str(len(prefixes[name].group()))], stdout=report, check=True)
    print(output)


if __name__ == "__main__":
    main()
