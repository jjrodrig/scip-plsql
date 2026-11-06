#!/usr/bin/env python3
"""Install N2 in a disposable Oracle database and compare PL/Scope.

Creates the N2_EXT schema; N2_ADMIN_PASSWORD supplies the SYSTEM password.
"""
import argparse
import json
import os
import pathlib
import re
import subprocess
import urllib.parse


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--container", required=True)
    parser.add_argument("--binary", type=pathlib.Path, required=True)
    parser.add_argument("--compare-bin", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--check", action="store_true", help="fail on baseline symbol/role regressions")
    args = parser.parse_args()
    uri = urllib.parse.urlsplit(os.environ.get("ORACLE_DSN", ""))
    if uri.scheme != "oracle" or not uri.username or not uri.password or not uri.path.strip("/"):
        parser.error("ORACLE_DSN must be oracle://user:password@host:port/service")
    user, password, service = urllib.parse.unquote(uri.username), urllib.parse.unquote(uri.password), uri.path.strip("/")
    if not re.fullmatch(r"[A-Za-z][A-Za-z0-9_$#]*", user) or not re.fullmatch(r"[A-Za-z0-9_$#]+", password) or not re.fullmatch(r"[A-Za-z0-9._-]+", service):
        parser.error("N2 requires SQL*Plus-safe username, password and service")
    admin = os.environ.get("N2_ADMIN_PASSWORD", "")
    if not re.fullmatch(r"[A-Za-z0-9_$#]+", admin):
        parser.error("N2_ADMIN_PASSWORD must provide the temporary Oracle SYSTEM password")
    fixture = pathlib.Path(__file__).resolve().parents[1] / "corpus/n2/fixtures"
    files = ["tables.sql", "address.typ", "account_trigger.trg", "ledger.pks", "ledger.pkb", "synonym.sql"]
    sql = (f"connect SYSTEM/{admin}@localhost/{service}\nwhenever sqlerror exit sql.sqlcode\n"
           f"CREATE USER n2_ext IDENTIFIED BY {password};\nGRANT CREATE SESSION, CREATE PROCEDURE TO n2_ext;\n"
           f"GRANT CREATE SYNONYM, CREATE TYPE, CREATE TRIGGER TO {user};\n"
           f"connect n2_ext/{password}@localhost/{service}\n"
           "alter session set PLSCOPE_SETTINGS='IDENTIFIERS:ALL, STATEMENTS:ALL';\n" +
           (fixture / "remote.pks").read_text() + "\n" + (fixture / "remote.pkb").read_text() +
           "\ndeclare n number; begin select count(*) into n from user_objects where object_name='N2_REMOTE' "
           "and object_type in ('PACKAGE','PACKAGE BODY') and status='VALID'; "
           "if n != 2 then raise_application_error(-20000,'N2 remote package failed to compile'); end if; end;\n/\n" +
           f"\nGRANT EXECUTE ON n2_remote TO {user};\n"
           f"connect {user}/{password}@localhost/{service}\n"
           "alter session set PLSCOPE_SETTINGS='IDENTIFIERS:ALL, STATEMENTS:ALL';\n" +
           "\n".join((fixture / path).read_text() for path in files) +
           "\nCREATE TABLE n2_external (external_id NUMBER PRIMARY KEY, external_score NUMBER);\n" +
           # The caller is compiled after its external dependency exists.
           (fixture / "client.prc").read_text() +
           "\ndeclare n number; begin select count(*) into n from user_objects where object_name in "
           "('N2_LEDGER','N2_CLIENT','N2_ADDRESS','N2_ACCOUNTS_BIU') and object_type in ('PACKAGE','PACKAGE BODY','PROCEDURE','TYPE','TRIGGER') and status='VALID'; "
           "if n != 5 then raise_application_error(-20000,'N2 objects failed to compile'); end if; end;\n/\nexit\n")
    subprocess.run(["docker", "exec", "-i", args.container, "sqlplus", "-s", "/nolog"], input=sql, text=True, check=True, stdout=subprocess.DEVNULL)
    output = args.output.resolve(); output.mkdir(parents=True, exist_ok=True)
    binary, comparer = args.binary.resolve(strict=True), args.compare_bin.resolve(strict=True)
    catalog = json.loads((fixture / "catalog.json").read_text())
    catalog["objects"].append({"schema": user.upper(), "name": "N2_EXTERNAL", "kind": "table", "columns": [{"name": "EXTERNAL_ID", "type": "NUMBER"}, {"name": "EXTERNAL_SCORE", "type": "NUMBER"}]})
    catalog_path = output / "catalog.json"; catalog_path.write_text(json.dumps(catalog, indent=2) + "\n")
    index = output / "index.scip"
    subprocess.run([str(binary), "index", "--database", "n2", "--package-version", "1", "--schema", user.upper(), "--catalog", str(catalog_path), "--output", str(index), str(fixture)], check=True)
    for owner, name in [(user.upper(), "N2_LEDGER"), (user.upper(), "N2_CLIENT"), (user.upper(), "N2_ADDRESS"), (user.upper(), "N2_ACCOUNTS_BIU"), ("N2_EXT", "N2_REMOTE")]:
        env = os.environ.copy()
        if owner == "N2_EXT":
            env["ORACLE_DSN"] = f"oracle://SYSTEM:{urllib.parse.quote(admin)}@{uri.netloc.rsplit('@', 1)[-1]}/{service}"
        subprocess.run([str(binary), "extract", "--schema", owner, "--object", name, "--kind", "identifiers", "--output", str(output / (name + ".json"))], check=True, env=env)
    entries = []
    for filename, name, kind in [("ledger.pks", "N2_LEDGER", "PACKAGE"), ("ledger.pkb", "N2_LEDGER", "PACKAGE BODY"), ("client.prc", "N2_CLIENT", "PROCEDURE"), ("address.typ", "N2_ADDRESS", "TYPE"), ("account_trigger.trg", "N2_ACCOUNTS_BIU", "TRIGGER"), ("remote.pks", "N2_REMOTE", "PACKAGE"), ("remote.pkb", "N2_REMOTE", "PACKAGE BODY")]:
        source = fixture / filename
        prefix = re.match(r"(?i)^\s*create\s+or\s+replace\s+", source.read_text())
        if not prefix: raise ValueError("unexpected fixture header")
        records = json.loads((output / (name + ".json")).read_text())
        if not any(row.get("object_type") == kind for row in records):
            raise RuntimeError(f"PL/Scope returned no {kind} records for {name}")
        entries.append({"document": filename, "source": str(source), "rows": str(output / (name + ".json")), "object_type": kind, "first_line_prefix": len(prefix.group())})
    prefix = "scip-plsql oracle n2 1 "
    schema = user.upper()
    anchors = [
        {"name": "N2_ACCOUNTS", "type": "TABLE", "symbol": prefix + schema + "/N2_ACCOUNTS#"},
        {"name": "ACCOUNT_ID", "type": "COLUMN", "symbol": prefix + schema + "/N2_ACCOUNTS#ACCOUNT_ID."},
        {"name": "BALANCE", "type": "COLUMN", "symbol": prefix + schema + "/N2_ACCOUNTS#BALANCE."},
        {"name": "N2_EXTERNAL", "type": "TABLE", "symbol": prefix + schema + "/N2_EXTERNAL#"},
        {"name": "EXTERNAL_ID", "type": "COLUMN", "symbol": prefix + schema + "/N2_EXTERNAL#EXTERNAL_ID."},
        {"name": "EXTERNAL_SCORE", "type": "COLUMN", "symbol": prefix + schema + "/N2_EXTERNAL#EXTERNAL_SCORE."},
        {"name": "ACCOUNT_ID", "type": "RECORD FIELD", "symbol": prefix + schema + "/N2_ACCOUNTS#ACCOUNT_ID."},
        {"name": "N2_ADDRESS", "type": "OBJECT", "symbol": prefix + schema + "/N2_ADDRESS#"},
        {"name": "STREET", "type": "OBJECT ATTRIBUTE", "symbol": prefix + schema + "/N2_ADDRESS#STREET."},
        {"name": "N2_ACCOUNTS_BIU", "type": "TRIGGER", "symbol": prefix + schema + "/N2_ACCOUNTS_BIU."},
        {"name": "N2_REMOTE", "type": "PACKAGE", "symbol": prefix + "N2_EXT/N2_REMOTE#"},
        {"name": "N2_R", "type": "SYNONYM", "symbol": prefix + "N2_EXT/N2_REMOTE#"},
        {"name": "PING", "type": "PROCEDURE", "symbol": prefix + "N2_EXT/N2_REMOTE#PING()."},
        {"document": "ledger.pks", "line": 1, "column": 9, "name": "N2_LEDGER", "type": "PACKAGE", "symbol": prefix + schema + "/N2_LEDGER#"},
        {"document": "ledger.pks", "line": 2, "column": 12, "name": "AMOUNT", "type": "FUNCTION", "symbol": prefix + schema + "/N2_LEDGER#AMOUNT()."},
        {"document": "ledger.pks", "line": 3, "column": 12, "name": "AMOUNT", "type": "FUNCTION", "symbol": prefix + schema + "/N2_LEDGER#AMOUNT(+1)."},
        {"document": "ledger.pks", "line": 4, "column": 13, "name": "CREDIT", "type": "PROCEDURE", "symbol": prefix + schema + "/N2_LEDGER#CREDIT()."},
    ]
    manifest = output / "manifest.json"
    roles = [
        {"document": "ledger.pkb", "line": 20, "column": 28, "name": "BALANCE", "role": "write"},
        {"document": "ledger.pkb", "line": 20, "column": 38, "name": "BALANCE", "role": "read"},
    ]
    manual = [
        {"document": "account_trigger.trg", "line": 5, "column": 8, "name": "BALANCE", "role": "write", "symbol": prefix + schema + "/N2_ACCOUNTS#BALANCE."},
        {"document": "account_trigger.trg", "line": 5, "column": 28, "name": "BALANCE", "role": "read", "symbol": prefix + schema + "/N2_ACCOUNTS#BALANCE."},
        {"document": "client.prc", "line": 3, "column": 3, "name": "L_ID", "role": "definition", "symbol": "local 1", "type_definition": prefix + schema + "/N2_ACCOUNTS#ACCOUNT_ID."},
        {"document": "client.prc", "line": 4, "column": 3, "name": "L_ROW", "role": "definition", "symbol": "local 2", "type_definition": prefix + schema + "/N2_ACCOUNTS#"},
        {"document": "client.prc", "line": 5, "column": 3, "name": "L_ADDRESS", "role": "definition", "symbol": "local 3", "type_definition": prefix + schema + "/N2_ADDRESS#"},
    ]
    manifest.write_text(json.dumps({"entries": entries, "bindings": anchors, "roles": roles, "manual": manual}, indent=2) + "\n")
    with (output / "comparison.json").open("w") as report:
        subprocess.run([str(comparer), "--index", str(index), "--manifest", str(manifest)], stdout=report, check=True)
    if args.check:
        result = json.loads((output / "comparison.json").read_text())
        bad = ("missing", "different_symbol", "source_mismatch", "anchor_conflict")
        checked = [row for row in result["items"] if row.get("role_source") == "fixture"]
        role_keys = lambda rows: {(row["document"], row["line"], row["column"], row["name"]) for row in rows}
        external = {row["name"] for row in result["items"] if row["status"] == "matched"}
        ambiguous = {(row["document"], row["line"], row["column"], row["name"]) for row in result["items"] if row["status"] == "ambiguous"}
        expected_ambiguous = {("ledger.pkb", 10, 12, "AMOUNT"), ("client.prc", 7, 25, "AMOUNT")}
        anchors_seen = all(any(row["status"] == "matched" and row["name"] == anchor["name"] and row["type"] == anchor["type"] and row.get("expected") == anchor["symbol"] and ("document" not in anchor or row["document"] == anchor["document"]) and ("line" not in anchor or row["line"] == anchor["line"]) and ("column" not in anchor or row["column"] == anchor["column"]) for row in result["items"]) for anchor in anchors)
        if result["counts"].get("matched", 0) < 56 or result["roles"].get("matched", 0) < 49 or any(result["counts"].get(key, 0) for key in bad) or result["roles"].get("mismatch", 0) or role_keys(checked) != role_keys(roles) or any(row["role"] != "matched" for row in checked) or not {"N2_EXTERNAL", "EXTERNAL_ID", "EXTERNAL_SCORE"} <= external or not anchors_seen or not ambiguous <= expected_ambiguous or len(result.get("manual", [])) != len(manual) or any(row["status"] != "matched" for row in result.get("manual", [])):
            raise SystemExit("N2 comparison failed; inspect comparison.json")
    print(output / "comparison.json")


if __name__ == "__main__":
    main()
