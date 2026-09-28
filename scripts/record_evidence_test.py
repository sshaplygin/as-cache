"""Reject incomplete or inconsistent retained evidence, even with valid hashes."""

import json
from copy import deepcopy
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import record_evidence
from record_evidence import combine, verify_manifest
from trace_inputs import CATALOG, digest


def batch_fixture():
    trace = {
        "trace": "a",
        "requests": 100,
        "distinct_keys": 20,
        "capacity": 10,
        "effective_sample_rate": 0.5,
        "fixed_hit_rate_percent": {
            name: {"runs": [20] * (5 if name in ("Random", "W-TinyLFU") else 1)}
            for name in (
                "LRU",
                "LFU",
                "2Q",
                "ARC",
                "TTL",
                "Random",
                "W-TinyLFU",
                "S3-FIFO",
                "SIEVE",
            )
        },
        "adaptive": [
            {
                "epochs_per_trace": epochs,
                "epoch_requests": 100 // epochs,
                "hit_rate_percent": {"runs": [20] * 5},
            }
            for epochs in (10, 20, 50)
        ],
        "observe_only": [{"hit_rate_percent": 20}] * 5,
        "lfu_sieve_diagnostic": {},
    }
    second = deepcopy(trace)
    second["trace"] = "b"
    return {
        "commit": "a" * 40,
        "tree_modified": False,
        "runs": 5,
        "settings": {"ShadowSampleRate": 0.05},
        "bandit": "seeded",
        "traces": [trace, second],
    }


def write_batches(directory, batches):
    tuning = [
        {
            "epochs": epochs,
            "strategy": strategy,
            "gates": gates,
            "hit_rate_percent": {"runs": [20] * 5},
        }
        for epochs in (10, 20, 50)
        for strategy in ("cold", "warm")
        for gates in (False, True)
    ]
    for number, batch in enumerate(batches, 1):
        (directory / f"traces-{number}.json").write_text(json.dumps(batch))
        (directory / f"traces-{number}-tuning.json").write_text(json.dumps(tuning))


def complete_manifest(directory):
    write_batches(directory, [batch_fixture() for _ in range(3)])
    combine(directory, "a" * 40)
    (directory / "bytes.json").write_text(json.dumps({"commit": "a" * 40}))
    for name in (
        "README.md",
        "bytes.log",
        "reference.log",
        "reference.tsv",
        "evidence-1.log",
        "evidence-2.log",
        "evidence-3.log",
    ):
        (directory / name).write_text("retained output\n")
    commands = [
        {"args": ["make", "verify-ref"], "log": "reference.log", "exit_code": 0},
        {
            "args": ["python3", "scripts/measure_bytes.py"],
            "log": "bytes.log",
            "exit_code": 0,
        },
    ] + [
        {"args": ["make", "evidence"], "log": f"evidence-{i}.log", "exit_code": 0}
        for i in range(1, 4)
    ]
    manifest = {
        "commit": "a" * 40,
        "files": json.loads(CATALOG.read_text())["files"],
        "commands": commands,
        "artifacts_sha256": {p.name: digest(p) for p in directory.iterdir()},
    }
    (directory / "manifest.json").write_text(json.dumps(manifest))
    return manifest


class EvidenceManifestTest(unittest.TestCase):
    def test_complete_dataset_requires_commands_and_matches_raw_batches(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            manifest = complete_manifest(directory)
            verify_manifest(directory)
            missing = deepcopy(manifest)
            missing["commands"] = []
            (directory / "manifest.json").write_text(json.dumps(missing))
            with self.assertRaisesRegex(ValueError, "required measurement commands"):
                verify_manifest(directory)
            traces = json.loads((directory / "traces.json").read_text())
            traces["traces"][0]["adaptive"][0]["hit_rate_percent"]["runs"][0] = 99
            (directory / "traces.json").write_text(json.dumps(traces))
            manifest["artifacts_sha256"]["traces.json"] = digest(
                directory / "traces.json"
            )
            (directory / "manifest.json").write_text(json.dumps(manifest))
            with self.assertRaisesRegex(ValueError, "differs from its raw batches"):
                verify_manifest(directory)

    def test_combining_rejects_changed_inventory_settings_and_sample_counts(self):
        mutations = {
            "missing trace": lambda batches: batches[0]["traces"].pop(),
            "changed settings": lambda batches: batches[1]["settings"].update(
                ShadowSampleRate=0.5
            ),
            "changed capacity": lambda batches: batches[1]["traces"][0].update(
                capacity=123
            ),
            "missing adaptive runs": lambda batches: batches[1]["traces"][0][
                "adaptive"
            ][0]["hit_rate_percent"].update(runs=[20]),
            "missing observe runs": lambda batches: batches[1]["traces"][0].update(
                observe_only=[]
            ),
            "extra fixed arm": lambda batches: batches[1]["traces"][0][
                "fixed_hit_rate_percent"
            ].update(extra={"runs": [20]}),
        }
        for description, mutate in mutations.items():
            with self.subTest(description), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                batches = [batch_fixture() for _ in range(3)]
                mutate(batches)
                write_batches(directory, batches)
                with self.assertRaises(ValueError):
                    combine(directory, "a" * 40)

    def test_untracked_source_prevents_recording(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            subprocess.run(["git", "init", "-q", str(directory)], check=True)
            (directory / "injected_test.go").write_text("package injected\n")
            with patch.object(record_evidence, "ROOT", directory):
                with self.assertRaisesRegex(ValueError, "commit.*changes"):
                    record_evidence.record(directory / "out")

    def test_ignored_go_source_prevents_recording(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            subprocess.run(["git", "init", "-q", str(directory)], check=True)
            (directory / ".git/info/exclude").write_text(
                "metrics/zzz_refute_probe_test.go\n"
            )
            (directory / "metrics").mkdir()
            (directory / "metrics/zzz_refute_probe_test.go").write_text(
                "package metrics\n"
            )
            with patch.object(record_evidence, "ROOT", directory):
                with self.assertRaisesRegex(
                    ValueError, "ignored build inputs.*zzz_refute"
                ):
                    record_evidence.record(directory / "out")

    def test_empty_inventory_cannot_certify_unmeasured_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name in ("traces.json", "tuning.json", "bytes.json"):
                (directory / name).write_text(json.dumps({"commit": "unmeasured"}))
            (directory / "manifest.json").write_text(
                json.dumps(
                    {"commit": "unmeasured", "artifacts_sha256": {}, "commands": []}
                )
            )
            with self.assertRaisesRegex(ValueError, "required artifacts"):
                verify_manifest(directory)


if __name__ == "__main__":
    unittest.main()
