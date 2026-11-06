import pathlib
import json
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import corpus

TOOLS = pathlib.Path(__file__).resolve().parent


class CorpusTests(unittest.TestCase):
    def test_n1_selects_pinned_tracked_sources(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            local_root = root / "checkouts"
            sources = []
            for name in ("first", "second"):
                repo = local_root / name
                repo.mkdir(parents=True)
                subprocess.run(["git", "init", "-q", str(repo)], check=True)
                (repo / "tracked.sql").write_text("SELECT 1 FROM dual;\n")
                subprocess.run(["git", "-C", str(repo), "add", "tracked.sql"], check=True)
                subprocess.run(["git", "-C", str(repo), "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "fixture"], check=True)
                commit = subprocess.check_output(["git", "-C", str(repo), "rev-parse", "HEAD"], text=True).strip()
                (repo / "untracked.sql").write_text("ignored")
                sources.append({"name": name, "commit": commit, "url": "unused"})
            manifest = root / "manifest.json"
            manifest.write_text(json.dumps({"sources": sources}))
            with patch.object(corpus, "MANIFEST", manifest):
                corpus.n1(root / "out", None, local_root, ["second"])
                self.assertEqual(sorted(p.name for p in (root / "out").iterdir()), ["second"])
                self.assertEqual(sorted(p.name for p in (root / "out" / "second").iterdir()), ["tracked.sql"])
                with self.assertRaises(ValueError):
                    corpus.n1(root / "bad", None, local_root, ["missing"])
                sources[0]["commit"] = "0" * 40
                manifest.write_text(json.dumps({"sources": sources}))
                with self.assertRaises(ValueError):
                    corpus.n1(root / "wrong", None, local_root, ["first"])

    def test_synthetic_is_reproducible_and_has_requested_lines(self):
        with tempfile.TemporaryDirectory() as tmp:
            roots = [pathlib.Path(tmp) / name for name in ("a", "b")]
            for root in roots:
                subprocess.run([sys.executable, str(TOOLS / "corpus.py"), "n3", str(root), "--units", "7", "--units-per-file", "3"], check=True)
            first = [(p.name, p.read_bytes()) for p in sorted(roots[0].iterdir())]
            second = [(p.name, p.read_bytes()) for p in sorted(roots[1].iterdir())]
            self.assertEqual(first, second)
            self.assertEqual(sum(data.count(b"\n") for _, data in first), 35)
            self.assertEqual(len(first), 3)

    def test_measure_restores_changed_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            corpus = root / "corpus"
            corpus.mkdir()
            sql = corpus / "one.sql"
            sql.write_text("BEGIN\n  NULL;\nEND;\n")
            binary = root / "fake-indexer"
            binary.write_text("#!/usr/bin/env python3\nimport pathlib,sys\na=sys.argv\npathlib.Path(a[a.index('--output')+1]).write_bytes(b'index')\nprint('  Files: 1', file=sys.stderr)\n")
            binary.chmod(0o755)
            subprocess.run([sys.executable, str(TOOLS / "measure.py"), "--binary", str(binary), "--corpus", str(corpus), "--output", str(root / "results"), "--label", "test", "--revision", "test", "--change", "one.sql"], check=True)
            self.assertEqual(sql.read_text(), "BEGIN\n  NULL;\nEND;\n")
            self.assertTrue((root / "results" / "changed.log").exists())


if __name__ == "__main__":
    unittest.main()
