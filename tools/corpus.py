#!/usr/bin/env python3
"""Materialize pinned N1 sources or generate a deterministic N3 workload."""
import argparse
import json
import pathlib
import subprocess
import tarfile
import tempfile

MANIFEST = pathlib.Path(__file__).resolve().parents[1] / "corpus/manifest.json"


def n1(destination, local, local_root, names):
    sources = json.loads(MANIFEST.read_text())["sources"]
    if names:
        unknown = set(names) - {source["name"] for source in sources}
        if unknown:
            raise ValueError(f"unknown sources: {', '.join(sorted(unknown))}")
        sources = [source for source in sources if source["name"] in names]
    if local and len(sources) != 1:
        raise ValueError("--local requires selecting one source with --source")
    destination.mkdir(parents=True, exist_ok=True)
    for source in sources:
        with tempfile.TemporaryDirectory() as tmp:
            repo = pathlib.Path(local) if local else pathlib.Path(local_root) / source["name"] if local_root else pathlib.Path(tmp) / "repo"
            if not local and not local_root:
                subprocess.run(["git", "init", "--quiet", str(repo)], check=True)
                subprocess.run(["git", "-C", str(repo), "fetch", "--quiet", "--depth", "1", source["url"], source["commit"]], check=True)
            commit = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD" if local or local_root else source["commit"]], text=True).strip()
            if commit != source["commit"]:
                raise ValueError(f"{source['name']}: expected {source['commit']}, got {commit}")
            target = destination / source["name"]
            target.mkdir(exist_ok=False)
            archive = pathlib.Path(tmp) / "source.tar"
            with archive.open("wb") as out:
                subprocess.run(["git", "-C", str(repo), "archive", commit], stdout=out, check=True)
            with tarfile.open(archive) as tar:
                tar.extractall(target, filter="data")


def n3(destination, units, per_file):
    if units < 1 or per_file < 1:
        raise ValueError("units and units-per-file must be positive")
    destination.mkdir(parents=True, exist_ok=False)
    for first in range(0, units, per_file):
        path = destination / f"workload_{first // per_file:05d}.sql"
        with path.open("w", encoding="utf-8", newline="\n") as out:
            for i in range(first, min(first + per_file, units)):
                out.write(f"CREATE PROCEDURE hr.p_{i} IS\nBEGIN\n  NULL;\nEND;\n/\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    source = sub.add_parser("n1")
    source.add_argument("destination", type=pathlib.Path)
    sources = source.add_mutually_exclusive_group()
    sources.add_argument("--local", help="existing checkout at the pinned commit; requires one --source")
    sources.add_argument("--local-root", help="directory containing checkouts named as in the manifest")
    source.add_argument("--source", action="append", help="materialize only this source; repeatable")
    synthetic = sub.add_parser("n3")
    synthetic.add_argument("destination", type=pathlib.Path)
    synthetic.add_argument("--units", type=int, default=200000, help="five lines per unit; default: 1 million lines")
    synthetic.add_argument("--units-per-file", type=int, default=1000)
    args = parser.parse_args()
    if args.command == "n1":
        n1(args.destination, args.local, args.local_root, args.source)
    else:
        n3(args.destination, args.units, args.units_per_file)


if __name__ == "__main__":
    main()
