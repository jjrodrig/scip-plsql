#!/usr/bin/env python3
"""Measure pinned N1 repositories independently, preserving per-source reports."""
import argparse
import json
import pathlib
import subprocess
import sys


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=pathlib.Path, required=True)
    parser.add_argument("--diagnostics-bin", type=pathlib.Path, required=True)
    parser.add_argument("--corpus", type=pathlib.Path, required=True)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--jobs", type=int, default=0)
    parser.add_argument("--timeout", type=int, default=1800)
    args = parser.parse_args()
    root = pathlib.Path(__file__).resolve().parents[1]
    manifest = json.loads((root / "corpus/manifest.json").read_text())
    args.output.mkdir(parents=True, exist_ok=True)
    statuses = {}
    for source in manifest["sources"]:
        name = source["name"]
        path = args.corpus / name
        print(f"Measuring {name} ({source['commit']})", flush=True)
        cmd = [sys.executable, str(root / "tools/measure.py"), "--binary", str(args.binary),
               "--diagnostics-bin", str(args.diagnostics_bin), "--corpus", str(path),
               "--output", str(args.output / name), "--label", name, "--revision", source["commit"],
               "--jobs", str(args.jobs), "--timeout", str(args.timeout)]
        statuses[name] = subprocess.run(cmd).returncode
        (args.output / "status.json").write_text(json.dumps(statuses, indent=2) + "\n")
    if any(statuses.values()):
        raise SystemExit("one or more repositories failed; see status.json and per-source reports")


if __name__ == "__main__":
    main()
