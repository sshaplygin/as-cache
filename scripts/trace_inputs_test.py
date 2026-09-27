"""Fetch regressions: cached garbage and interrupted downloads must fail closed."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class TraceInputsTest(unittest.TestCase):
    def test_cached_garbage_is_rejected_without_network(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            manifest = ROOT / "scripts/trace-inputs.json"
            if not manifest.exists():
                manifest = ROOT / "bench/results/2026-09-27/manifest.json"
            for item in json.loads(manifest.read_text())["files"]:
                (directory / item["file"]).write_text("garbage")
            result = subprocess.run(
                ["bash", str(ROOT / "scripts/fetch-traces.sh"), temporary],
                capture_output=True,
                text=True,
                timeout=20,
            )
            self.assertNotEqual(0, result.returncode)
            self.assertIn("twitter_cluster052.csv", result.stdout + result.stderr)
            self.assertIn("FAIL", result.stdout + result.stderr)

    def test_interrupted_download_does_not_create_final_file(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            curl = directory / "curl"
            curl.write_text(
                '#!/bin/bash\nwhile [ "$#" -gt 0 ]; do\n'
                'if [ "$1" = -o ]; then shift; printf partial > "$1"; fi\n'
                "shift\ndone\nexit 42\n"
            )
            curl.chmod(0o755)
            traces = directory / "traces"
            result = subprocess.run(
                ["bash", str(ROOT / "scripts/fetch-traces.sh"), str(traces)],
                env=dict(os.environ, PATH=temporary + ":" + os.environ["PATH"]),
                capture_output=True,
                text=True,
                timeout=20,
            )
            self.assertNotEqual(0, result.returncode)
            self.assertFalse((traces / "twitter_cluster052.csv").exists())
            self.assertIn("exited with status", result.stderr)


if __name__ == "__main__":
    unittest.main()
