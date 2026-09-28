"""Run and retain one current, reproducible three-batch evidence dataset."""

import argparse
from copy import deepcopy
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import subprocess
import time

from render_evidence import render
from trace_inputs import CATALOG, digest, verify

ROOT = Path(__file__).resolve().parent.parent


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def verify_manifest(directory):
    manifest = json.loads((directory / "manifest.json").read_text())
    for name, expected in manifest["artifacts_sha256"].items():
        if digest(directory / name) != expected:
            raise ValueError(f"artifact hash mismatch: {name}")
    for name in ("traces.json", "tuning.json", "bytes.json"):
        data = json.loads((directory / name).read_text())
        if data["commit"] != manifest["commit"]:
            raise ValueError(f"measurement commit mismatch: {name}")
    if not all(run["exit_code"] == 0 for run in manifest["commands"]):
        raise ValueError("a recorded measurement command failed")
    print(
        f"Verified {len(manifest['artifacts_sha256'])} artifacts at {manifest['commit']}"
    )


def combine(directory, commit):
    batches = [
        json.loads((directory / f"traces-{i}.json").read_text()) for i in range(1, 4)
    ]
    for batch in batches:
        if batch["commit"] != commit or batch["tree_modified"]:
            raise ValueError("measurement came from another commit or a dirty tree")
    combined = deepcopy(batches[0])
    combined["batches"] = 3
    combined["runs_per_batch"] = combined.pop("runs")
    for index, trace in enumerate(combined["traces"]):
        for batch in batches[1:]:
            other = batch["traces"][index]
            if (
                other["trace"] != trace["trace"]
                or other["requests"] != trace["requests"]
            ):
                raise ValueError("trace inventory changed between runs")
            for name in trace["fixed_hit_rate_percent"]:
                trace["fixed_hit_rate_percent"][name]["runs"].extend(
                    other["fixed_hit_rate_percent"][name]["runs"]
                )
            for adaptive, repeated in zip(
                trace["adaptive"], other["adaptive"], strict=True
            ):
                if adaptive["epoch_requests"] != repeated["epoch_requests"]:
                    raise ValueError("epoch settings changed between runs")
                adaptive["hit_rate_percent"]["runs"].extend(
                    repeated["hit_rate_percent"]["runs"]
                )
            trace["observe_only"].extend(other["observe_only"])
            if trace["lfu_sieve_diagnostic"] != other["lfu_sieve_diagnostic"]:
                raise ValueError("deterministic diagnostics changed between runs")
    (directory / "traces.json").write_text(json.dumps(combined, indent=2) + "\n")
    tuning = json.loads((directory / "traces-1-tuning.json").read_text())
    for number in (2, 3):
        other = json.loads((directory / f"traces-{number}-tuning.json").read_text())
        for row, repeated in zip(tuning, other, strict=True):
            if any(
                row[key] != repeated[key] for key in row if key != "hit_rate_percent"
            ):
                raise ValueError("tuning settings changed between runs")
            row["hit_rate_percent"]["runs"].extend(repeated["hit_rate_percent"]["runs"])
    (directory / "tuning.json").write_text(
        json.dumps({"commit": commit, "records": tuning}, indent=2) + "\n"
    )


def record(directory):
    if git("status", "--porcelain", "--untracked-files=no"):
        raise ValueError("commit tracked changes before measuring")
    if directory.exists() and any(directory.iterdir()):
        raise ValueError(
            "output directory must be empty; old data must not be mixed into a new measurement"
        )
    directory.mkdir(parents=True, exist_ok=True)
    traces = Path(os.environ["AS_CACHE_TRACES"]).resolve()
    files = json.loads(CATALOG.read_text())["files"]
    for entry in files:
        verify(traces / entry["file"], entry)
    lcs = Path(
        os.environ.get("AS_CACHE_LIBCACHESIM", ROOT / ".tools/libCacheSim")
    ).resolve()
    manifest = {
        "commit": git("rev-parse", "HEAD"),
        "started_at": datetime.now(timezone.utc).isoformat(),
        "platform": platform.platform(),
        "machine": platform.machine(),
        "go": subprocess.check_output(["go", "version"], text=True).strip(),
        "files": files,
        "commands": [],
    }
    env = dict(
        os.environ,
        AS_CACHE_TRACES=str(traces),
        AS_CACHE_LIBCACHESIM=str(lcs),
        AS_CACHE_REFERENCE_OUT=str(directory / "reference.tsv"),
    )

    def command(arguments, log):
        started = time.monotonic()
        print(f"Running {' '.join(arguments)} -> {log}", flush=True)
        with (directory / log).open("w") as output:
            result = subprocess.run(
                arguments, cwd=ROOT, env=env, stdout=output, stderr=subprocess.STDOUT
            )
        manifest["commands"].append(
            {
                "args": arguments,
                "log": log,
                "exit_code": result.returncode,
                "duration_seconds": time.monotonic() - started,
            }
        )
        (directory / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
        if result.returncode:
            raise ValueError(
                f"measurement failed; retained {log}, exit {result.returncode}"
            )

    command(["make", "verify-ref"], "reference.log")
    manifest["libcachesim_commit"] = subprocess.check_output(
        ["git", "-C", str(lcs), "rev-parse", "HEAD"], text=True
    ).strip()
    manifest["libcachesim_binary_sha256"] = digest(lcs / "_build/bin/cachesim")
    command(
        [
            "python3",
            "scripts/measure_bytes.py",
            str(traces),
            str(directory / "bytes.json"),
            "--libcachesim",
            str(lcs),
        ],
        "bytes.log",
    )
    for number in range(1, 4):
        env["AS_CACHE_EVIDENCE_OUT"] = str(directory / f"traces-{number}.json")
        command(["make", "evidence"], f"evidence-{number}.log")
        if git("status", "--porcelain", "--untracked-files=no"):
            raise ValueError("tracked measurement sources changed during the run")
    combine(directory, manifest["commit"])
    render(directory)
    manifest["completed_at"] = datetime.now(timezone.utc).isoformat()
    manifest["artifacts_sha256"] = {
        path.name: digest(path)
        for path in sorted(directory.iterdir())
        if path.is_file() and path.name != "manifest.json"
    }
    (directory / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    verify_manifest(directory)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    choice = parser.add_mutually_exclusive_group(required=True)
    choice.add_argument("--out", type=Path)
    choice.add_argument("--verify", type=Path)
    args = parser.parse_args()
    try:
        if args.verify:
            verify_manifest(args.verify)
        else:
            record(args.out.resolve())
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"FAIL: {error}\n")


if __name__ == "__main__":
    main()
