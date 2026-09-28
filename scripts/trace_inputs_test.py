"""Fetch regressions: cached garbage and interrupted downloads must fail closed."""

import gzip
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

    def test_reference_rejects_extra_msr_volume(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            specs = [
                ("twitter_cluster052.csv", "0,key,0\n", 10000),
                ("lirs_loop.trace.gz", "1\n", 500),
                ("lirs_2_pools.trace.gz", "1\n", 1000),
                ("arc_p3.gz", "1 1 0 0\n", 20000),
                ("arc_oltp.gz", "1 1 0 0\n", 20000),
                (
                    "meta_kvcache_202206_1.csv",
                    "key,op,size,op_count,key_size\n1,GET,1,1,1\n",
                    10000,
                ),
            ]
            for volume in ("hm_0", "prn_0", "proj_0", "src1_2", "usr_0", "web_0"):
                specs.append((f"msr_{volume}.csv.gz", "0,host,0,Read,0,512,0\n", 20000))
            for name, contents, _ in specs:
                path = directory / name
                if name.endswith(".gz"):
                    path.write_bytes(gzip.compress(contents.encode()))
                else:
                    path.write_text(contents)
            (directory / "msr_zzz_9.csv.gz").write_bytes(
                gzip.compress(b"0,host,0,Read,0,512,0\n")
            )
            reference = directory / "reference.tsv"
            reference.write_text(
                "".join(f"{name}\t{capacity}\t1\t1.0\n" for name, _, capacity in specs)
            )
            result = subprocess.run(
                ["go", "test", "-count=1", "-run", "^TestLRUMatchesReference$", "."],
                cwd=ROOT / "bench",
                text=True,
                capture_output=True,
                env=dict(
                    os.environ,
                    AS_CACHE_TRACES=temporary,
                    AS_CACHE_LRU_REFERENCE=str(reference),
                ),
                timeout=60,
            )
            self.assertNotEqual(0, result.returncode, result.stdout + result.stderr)
            self.assertIn("msr_zzz_9.csv.gz", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
