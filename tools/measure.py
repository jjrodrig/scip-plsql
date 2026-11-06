#!/usr/bin/env python3
"""Measure cold, warm and one-file-change indexing; emit JSON and raw logs."""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import platform
import re
import subprocess
import tempfile
import time


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=pathlib.Path, required=True)
    parser.add_argument("--corpus", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--label", required=True)
    parser.add_argument("--revision", required=True, help="manifest commit or synthetic generator parameters")
    parser.add_argument("--jobs", type=int, default=0)
    parser.add_argument("--timeout", type=int, default=1800)
    parser.add_argument("--change", type=pathlib.Path, help="relative .sql file to modify temporarily for PR measurement")
    args = parser.parse_args()
    binary, corpus, output = args.binary.resolve(strict=True), args.corpus.resolve(strict=True), args.output.resolve()
    if output == corpus or corpus in output.parents:
        parser.error("output must be outside the corpus")
    if args.jobs < 0 or args.timeout < 1:
        parser.error("jobs must be nonnegative and timeout positive")
    output.mkdir(parents=True, exist_ok=True)
    report = {"label": args.label, "revision": args.revision, "utc": datetime.datetime.now(datetime.timezone.utc).isoformat(),
              "binary_sha256": digest(binary), "platform": platform.platform(), "cpu_count": os.cpu_count(),
              "jobs": args.jobs, "timeout_seconds": args.timeout,
              "change_file": str(args.change) if args.change else None, "runs": {}}
    with tempfile.TemporaryDirectory(prefix="scip-measure-") as tmp:
        tmp = pathlib.Path(tmp)
        def run(name):
            index = tmp / (name + ".scip")
            cmd = [str(binary), "index", "--database", "benchmark", "--package-version", "1", "--schema", "HR",
                   "--jobs", str(args.jobs), "--cache-dir", str(tmp / "cache"), "--cache-stats", "--metrics",
                   "--output", str(index), str(corpus)]
            started = time.monotonic()
            try:
                result = subprocess.run(cmd, capture_output=True, text=True, timeout=args.timeout)
                log = result.stdout + result.stderr
                entry = {"exit_code": result.returncode, "wall_seconds": time.monotonic() - started}
            except subprocess.TimeoutExpired as exc:
                log = (exc.stdout or b"").decode(errors="replace") + (exc.stderr or b"").decode(errors="replace")
                entry = {"timeout": True, "wall_seconds": time.monotonic() - started}
            (output / (name + ".log")).write_text(log)
            entry["metrics"] = dict(re.findall(r"^\s{2}([^:\n]+):\s*(.+)$", log, re.MULTILINE))
            if index.exists():
                entry["index_sha256"] = digest(index)
                entry["index_bytes"] = index.stat().st_size
            report["runs"][name] = entry
            (output / "report.json").write_text(json.dumps(report, indent=2) + "\n")
            if entry.get("exit_code") != 0:
                raise RuntimeError(f"{name} failed or timed out; see {output}")
            return entry
        cold = run("cold")
        warm = run("warm")
        report["cold_warm_identical"] = cold["index_sha256"] == warm["index_sha256"]
        (output / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        if not report["cold_warm_identical"]:
            raise RuntimeError("cold and warm indexes differ")
        if args.change:
            path = (corpus / args.change).resolve(strict=True)
            if corpus not in path.parents or path.suffix.lower() != ".sql":
                parser.error("--change must point to a .sql file inside the corpus")
            original = path.read_bytes()
            try:
                # Add an executable statement to the first synthetic procedure.
                if b"  NULL;\n" not in original:
                    raise ValueError("--change file must contain a synthetic NULL statement")
                path.write_bytes(original.replace(b"  NULL;\n", b"  NULL;\n  NULL; -- benchmark PR change\n", 1))
                run("changed")
            finally:
                path.write_bytes(original)
    print(output / "report.json")


if __name__ == "__main__":
    main()
