"""Run and retain one current, reproducible three-batch evidence dataset."""

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import time

from evidence_batches import pooled_results
from render_evidence import render
from trace_inputs import CATALOG, digest, verify

ROOT = Path(__file__).resolve().parent.parent
REQUIRED_ARTIFACTS = {
    "README.md",
    "bytes.json",
    "bytes.log",
    "reference.tsv",
    "reference.log",
    "traces.json",
    "tuning.json",
} | {
    name
    for number in range(1, 4)
    for name in (
        f"evidence-{number}.log",
        f"traces-{number}.json",
        f"traces-{number}-tuning.json",
    )
}


def git(*args):
    return subprocess.check_output(["git", *args], cwd=ROOT, text=True).strip()


def require_committed_sources():
    if git("status", "--porcelain", "--untracked-files=all"):
        raise ValueError("commit all source changes before measuring")
    ignored = git(
        "ls-files",
        "--others",
        "--ignored",
        "--exclude-standard",
        "--",
        "*.go",
        "*.c",
        "*.cc",
        "*.cpp",
        "*.h",
        "*.s",
        "*.S",
        "*.syso",
        "go.mod",
        "go.sum",
        "go.work",
    ).splitlines()
    # Go's ./... traversal ignores dot/underscore directories. This excludes
    # tool checkouts and report worktrees, but catches ignored package inputs.
    build_inputs = [
        name
        for name in ignored
        if not any(part.startswith((".", "_")) for part in Path(name).parts)
    ]
    if build_inputs:
        raise ValueError(
            f"ignored build inputs are not committed: {', '.join(build_inputs)}"
        )


def verify_manifest(directory):
    manifest = json.loads((directory / "manifest.json").read_text())
    artifacts = manifest.get("artifacts_sha256", {})
    if not REQUIRED_ARTIFACTS.issubset(artifacts):
        raise ValueError("manifest is missing required artifacts")
    actual = {
        p.name for p in directory.iterdir() if p.is_file() and p.name != "manifest.json"
    }
    if set(artifacts) != actual:
        raise ValueError("artifact inventory differs from retained files")
    if not re.fullmatch(r"[0-9a-f]{40}", manifest.get("commit", "")):
        raise ValueError("manifest has no valid source commit")
    if manifest.get("files") != json.loads(CATALOG.read_text())["files"]:
        raise ValueError("input inventory differs from the pinned trace catalog")
    commands = manifest.get("commands", [])
    expected_logs = ["reference.log", "bytes.log"] + [
        f"evidence-{i}.log" for i in range(1, 4)
    ]
    if [run.get("log") for run in commands] != expected_logs:
        raise ValueError("manifest is missing required measurement commands")
    for index, run in enumerate(commands):
        expected = ["make", "verify-ref"] if index == 0 else ["make", "evidence"]
        if index == 1:
            if run.get("args", [])[:2] != ["python3", "scripts/measure_bytes.py"]:
                raise ValueError("unexpected byte measurement command")
        elif run.get("args") != expected:
            raise ValueError("unexpected reference or evidence command")
        if run.get("exit_code") != 0:
            raise ValueError("a recorded measurement command failed")
    for name, expected in artifacts.items():
        if digest(directory / name) != expected:
            raise ValueError(f"artifact hash mismatch: {name}")
    for name in (
        "traces.json",
        "tuning.json",
        "bytes.json",
        *[f"traces-{i}.json" for i in range(1, 4)],
    ):
        data = json.loads((directory / name).read_text())
        if data["commit"] != manifest["commit"]:
            raise ValueError(f"measurement commit mismatch: {name}")
        if name.startswith("traces") and data["tree_modified"]:
            raise ValueError(f"measurement used a dirty tree: {name}")
    traces, tuning = pooled_results(directory, manifest["commit"])
    for name, expected in (("traces.json", traces), ("tuning.json", tuning)):
        if json.loads((directory / name).read_text()) != expected:
            raise ValueError(f"pooled artifact differs from its raw batches: {name}")
    print(
        f"Verified {len(manifest['artifacts_sha256'])} artifacts at {manifest['commit']}"
    )


def combine(directory, commit):
    traces, tuning = pooled_results(directory, commit)
    (directory / "traces.json").write_text(json.dumps(traces, indent=2) + "\n")
    (directory / "tuning.json").write_text(json.dumps(tuning, indent=2) + "\n")


def refresh_report(directory):
    """Regenerate presentation without rerunning or changing measurements."""
    if git("status", "--porcelain", "--untracked-files=all", "--", "scripts"):
        raise ValueError("commit report-generator changes before rendering")
    verify_manifest(directory)
    render(directory)
    manifest = json.loads((directory / "manifest.json").read_text())
    manifest["report_generator_commit"] = git("rev-parse", "HEAD")
    manifest["artifacts_sha256"]["README.md"] = digest(directory / "README.md")
    (directory / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    verify_manifest(directory)


def record(directory):
    require_committed_sources()
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
        require_committed_sources()
        if git("rev-parse", "HEAD") != manifest["commit"]:
            raise ValueError("measurement sources changed during the run")
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
    choice.add_argument("--render", type=Path)
    args = parser.parse_args()
    try:
        if args.verify:
            verify_manifest(args.verify)
        elif args.render:
            refresh_report(args.render)
        else:
            record(args.out.resolve())
    except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"FAIL: {error}\n")


if __name__ == "__main__":
    main()
