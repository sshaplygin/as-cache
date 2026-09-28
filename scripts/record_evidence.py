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
import sys
import tempfile

from committed_source import CommittedSource, git_environment

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
    return subprocess.check_output(
        ["git", *args], cwd=ROOT, text=True, env=git_environment()
    ).strip()


def require_committed_sources():
    # Called only inside the private snapshot, never over the preserved developer tree.
    if git("status", "--porcelain", "--untracked-files=all"):
        raise ValueError("measurement snapshot sources changed")
    ignored = git(
        "ls-files", "--others", "--ignored", "--exclude-standard"
    ).splitlines()
    # Go generates this workspace checksum file from committed module requirements.
    unexpected = [name for name in ignored if name != "go.work.sum"]
    if unexpected:
        raise ValueError(f"unexpected ignored snapshot inputs: {', '.join(unexpected)}")


def validate_output(directory):
    directory = directory.resolve()
    if directory.exists() and (not directory.is_dir() or any(directory.iterdir())):
        raise ValueError(
            "output directory must be empty; old data must not be mixed into a new measurement"
        )
    if directory == ROOT.resolve():
        raise ValueError("output must not be the repository root")
    if directory.is_relative_to(ROOT.resolve()):
        relative = directory.relative_to(ROOT.resolve()).as_posix()
        ignored = subprocess.run(
            ["git", "check-ignore", "--quiet", "--no-index", relative + "/"],
            cwd=ROOT,
            env=git_environment(),
        )
        if ignored.returncode != 0:
            raise ValueError(
                "output inside the repository must be ignored; use an external empty directory"
            )
    return directory


def record(directory):
    directory = validate_output(directory)
    traces = Path(os.environ["AS_CACHE_TRACES"]).resolve()
    lcs = Path(
        os.environ.get("AS_CACHE_LIBCACHESIM", ROOT / ".tools/libCacheSim")
    ).resolve()
    snapshot = CommittedSource(ROOT)
    with tempfile.TemporaryDirectory(prefix="as-cache-measure-") as temporary:
        source = snapshot.checkout(Path(temporary) / "source")
        print(
            f"Measuring committed snapshot {snapshot.commit} at {source}; developer files are excluded",
            flush=True,
        )
        env = dict(
            git_environment(),
            AS_CACHE_TRACES=str(traces),
            AS_CACHE_LIBCACHESIM=str(lcs),
            PYTHONPATH=str(source / "scripts"),
            PYTHONDONTWRITEBYTECODE="1",
            GOWORK=str(source / "go.work") if (source / "go.work").exists() else "off",
            GOFLAGS="",
            GOENV="off",
        )
        command = [
            sys.executable,
            "-B",
            "-c",
            "import sys; from pathlib import Path; from record_evidence import measure; measure(Path(sys.argv[1]))",
            str(directory),
        ]
        subprocess.run(command, cwd=source, env=env, check=True)


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


def measure(directory):
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
