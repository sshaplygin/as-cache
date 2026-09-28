"""Reject incomplete or inconsistent retained evidence, even with valid hashes."""

import json
from copy import deepcopy
from pathlib import Path
import tempfile
import shutil
import subprocess
import unittest
from unittest.mock import patch

from record_evidence import combine, refresh_report, verify_manifest
from render_evidence import render, report_text
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
        "policy_names": {
            "TinyLFU": "W-TinyLFU",
            "TwoQueue": "2Q",
            "LRU": "LRU",
            "LFU": "LFU",
        },
        "observe_only": [{"hit_rate_percent": 20, "best_policy_name": "LRU"}] * 5,
        "lfu_sieve_diagnostic": {
            name: 0
            for name in (
                "lfu_hits",
                "sieve_hits",
                "fifo_hits",
                "different_lfu_sieve_decisions",
                "sieve_reference_disagreements",
            )
        },
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
    (directory / "bytes.json").write_text(
        json.dumps(
            {
                "commit": "a" * 40,
                "libcachesim_commit": "b" * 40,
                "requests": 100,
                "mean_first_object_bytes": 200,
                "rows": [
                    {
                        "object_capacity": 10,
                        "byte_capacity": 2000,
                        "objects": {"miss_ratio": 0.5},
                        "bytes": {"miss_ratio": 0.6},
                        "request_miss_delta_pp": 10,
                    }
                ],
            }
        )
    )
    for name in (
        "bytes.log",
        "reference.log",
        "reference.tsv",
        "evidence-1.log",
        "evidence-2.log",
        "evidence-3.log",
    ):
        (directory / name).write_text("retained output\n")
    render(directory)
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

    def test_report_tamper_fails_even_with_updated_hash(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            manifest = complete_manifest(directory)
            readme = directory / "README.md"
            readme.write_text(readme.read_text().replace("20.00%", "99.00%", 1))
            manifest["artifacts_sha256"]["README.md"] = digest(readme)
            (directory / "manifest.json").write_text(json.dumps(manifest))
            before = {p.name: p.read_bytes() for p in directory.iterdir()}
            with self.assertRaisesRegex(ValueError, "README differs from the report"):
                verify_manifest(directory)
            self.assertEqual(
                before, {p.name: p.read_bytes() for p in directory.iterdir()}
            )

    def test_refresh_accepts_old_template_without_changing_measurements(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            complete_manifest(directory)
            before = {
                p.name: p.read_bytes()
                for p in directory.iterdir()
                if p.name not in ("README.md", "manifest.json")
            }

            def next_template(folder):
                return report_text(folder) + "New presentation.\n"

            with (
                patch(
                    "record_evidence.git",
                    side_effect=lambda *args: "b" * 40
                    if args[0] == "rev-parse"
                    else "",
                ),
                patch("record_evidence.report_text", side_effect=next_template),
                patch("render_evidence.report_text", side_effect=next_template),
            ):
                with self.assertRaisesRegex(
                    ValueError, "README differs from the report"
                ):
                    verify_manifest(directory)
                refresh_report(directory)
                verify_manifest(directory)
            self.assertEqual(
                before,
                {
                    p.name: p.read_bytes()
                    for p in directory.iterdir()
                    if p.name in before
                },
            )
            manifest = json.loads((directory / "manifest.json").read_text())
            self.assertEqual("a" * 40, manifest["commit"])
            self.assertEqual("b" * 40, manifest["report_generator_commit"])

    def test_refresh_rejects_modified_measurements_before_writing_report(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            complete_manifest(directory)
            (directory / "traces.json").write_text("modified input")
            before = (directory / "README.md").read_bytes()
            with (
                patch("record_evidence.git", return_value=""),
                self.assertRaisesRegex(
                    ValueError, "artifact hash mismatch: traces.json"
                ),
            ):
                refresh_report(directory)
            self.assertEqual(before, (directory / "README.md").read_bytes())

    def test_report_preserves_tied_ranges_and_defines_advice_denominators(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            complete_manifest(directory)
            data = json.loads((directory / "traces.json").read_text())
            for trace in data["traces"]:
                fixed = trace["fixed_hit_rate_percent"]
                fixed["LRU"]["runs"] = [38, 40, 42]
                fixed["LFU"]["runs"] = [39, 40, 41]
                fixed["2Q"]["runs"] = [30] * 3
                fixed["W-TinyLFU"]["runs"] = [9, 10, 11] * 5
                fixed["Random"]["runs"] = [8, 10, 12] * 5
                for cell in trace["adaptive"]:
                    cell["hit_rate_percent"]["runs"] = [9] * 15
            first, second = data["traces"]
            for trace, choices in (
                (first, ["TinyLFU", "TwoQueue", "TinyLFU", "TwoQueue", "LRU"] * 3),
                (second, ["LRU", "LRU", "LRU", "LFU", "LFU"] * 3),
            ):
                trace["observe_only"] = [
                    {"hit_rate_percent": 40, "best_policy_name": choice}
                    for choice in choices
                ]
            (directory / "traces.json").write_text(json.dumps(data))
            result = report_text(directory)
            self.assertIn(
                "LFU 40.00% [39.00–41.00]<br>LRU 40.00% [38.00–42.00]", result
            )
            self.assertIn(
                "Random 10.00% [8.00–12.00]<br>W-TinyLFU 10.00% [9.00–11.00]", result
            )
            self.assertIn("versus Random 10.00% [8.00–12.00]<br>W-TinyLFU", result)
            self.assertIn("Retrospective modal agreement: 1/2 traces.", result)
            self.assertIn("18/30 (60.00%)", result)
            self.assertIn("| a | 2Q 30.00%", result)
            self.assertIn("| a | W-TinyLFU 10.00%", result)
            self.assertIn("| -30.0000 pp |", result)
            self.assertIn("on 2/2 traces at every tested epoch setting", result)

    def test_make_gate_is_read_only_and_rejects_tampering(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            repository = Path(__file__).resolve().parent.parent
            shutil.copyfile(repository / "Makefile", root / "Makefile")
            shutil.copytree(
                repository / "scripts",
                root / "scripts",
                ignore=shutil.ignore_patterns("__pycache__"),
            )
            directory = root / "bench/results/current"
            directory.mkdir(parents=True)
            manifest = complete_manifest(directory)
            before = {p.name: p.read_bytes() for p in directory.iterdir()}
            result = subprocess.run(
                ["make", "evidence-check"], cwd=root, capture_output=True, text=True
            )
            self.assertEqual(0, result.returncode, result.stdout + result.stderr)
            self.assertEqual(
                before, {p.name: p.read_bytes() for p in directory.iterdir()}
            )
            readme = directory / "README.md"
            readme.write_text(readme.read_text() + "Unsupported conclusion.\n")
            manifest["artifacts_sha256"]["README.md"] = digest(readme)
            (directory / "manifest.json").write_text(json.dumps(manifest))
            result = subprocess.run(
                ["make", "evidence-check"], cwd=root, capture_output=True, text=True
            )
            self.assertNotEqual(0, result.returncode)
            self.assertIn("README differs from the report", result.stderr)

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
