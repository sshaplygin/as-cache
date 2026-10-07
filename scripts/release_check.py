#!/usr/bin/env python3
"""Build release candidates as consumers, without repository replacements.

Candidate mode serves one resolved HEAD snapshot as module zips from a temporary file proxy.
Published mode downloads actual versions. Neither mode changes tags or go.mod.
"""

import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import zipfile

from committed_source import CommittedSource

MODULE = "github.com/sshaplygin/as-cache"
# FIFO stays experimental; it must not enter any published dependency graph.
EXCLUDED = {"bench", "examples/basic", "examples/migration", "policies/fifo"}
ROOT = Path(__file__).resolve().parent.parent


def run(*args, cwd=ROOT, env=None):
    result = subprocess.run(
        args,
        cwd=cwd,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
    )
    if result.returncode:
        raise RuntimeError(
            f"{' '.join(args)} failed ({result.returncode}):\n{result.stdout}"
        )
    return result.stdout


def module_path(directory):
    return MODULE if directory == "." else f"{MODULE}/{directory}"


def module_directories(source):
    return sorted(
        str(path.parent.relative_to(source)) for path in source.rglob("go.mod")
    )


def publishable(source):
    return [
        directory
        for directory in module_directories(source)
        if directory not in EXCLUDED
    ]


def preflight(version, source):
    expected = {module_path(d) for d in publishable(source)}
    env = dict(os.environ, GOWORK="off", GOFLAGS="")
    for directory in publishable(source):
        path = source / directory
        metadata = json.loads(run("go", "mod", "edit", "-json", cwd=path, env=env))
        if metadata["Module"]["Path"] != module_path(directory):
            raise RuntimeError(f"{directory}: unexpected module path")
        if not any(p.is_file() and p.stat().st_size for p in path.glob("LICENSE*")):
            raise RuntimeError(f"{directory}: missing tracked nonempty LICENSE")
        if metadata.get("Replace"):
            raise RuntimeError(
                f"{directory}: published go.mod must not contain replace directives"
            )
        for dependency in metadata.get("Require") or []:
            name = dependency["Path"]
            if name != MODULE and not name.startswith(MODULE + "/"):
                continue
            if name not in expected:
                raise RuntimeError(f"{directory}: {name} is outside the release set")
            if dependency["Version"] != version:
                raise RuntimeError(
                    f"{directory}: {name}@{dependency['Version']} must use release version {version}"
                )
        print(f"  metadata ok: {directory}", flush=True)


def candidate_proxy(destination, version, source):
    # The exported source contains only blobs from the same resolved commit.
    tracked = sorted(
        path.relative_to(source) for path in source.rglob("*") if path.is_file()
    )
    module_dirs = {p.parent for p in tracked if p.name == "go.mod"}
    for directory in publishable(source):
        base = Path(directory)
        name = module_path(directory)
        target = destination / name / "@v"
        target.mkdir(parents=True)
        (target / f"{version}.mod").write_bytes((source / base / "go.mod").read_bytes())
        info = {"Version": version, "Time": datetime.now(timezone.utc).isoformat()}
        (target / f"{version}.info").write_text(json.dumps(info))
        (target / "list").write_text(version + "\n")
        with zipfile.ZipFile(
            target / f"{version}.zip", "w", zipfile.ZIP_DEFLATED
        ) as archive:
            for file in tracked:
                if base != Path(".") and base not in file.parents:
                    continue
                if any(
                    child != base
                    and child in file.parents
                    and (base == Path(".") or base in child.parents)
                    for child in module_dirs
                ):
                    continue
                archive.write(
                    source / file, f"{name}@{version}/{file.relative_to(base)}"
                )


def check_consumers(work, version, published, source):
    # A fresh module cache prevents candidate versions from contaminating a
    # developer's real cache or a prior download from hiding a missing version.
    # Deliberately download dependencies per consumer: sharing a cache can mask
    # nested-module discovery failures after a parent module was installed.
    # The extra network cost buys independent installation evidence.
    env = dict(os.environ, GOWORK="off", GOFLAGS="", GOPRIVATE="", GONOPROXY="none")
    upstream = os.environ.get("GOPROXY", "https://proxy.golang.org,direct")
    if published:
        env["GOPROXY"] = upstream
        env["GONOSUMDB"] = ""
        print(f"Checking published {version} through {upstream}", flush=True)
    else:
        proxy = work / "proxy"
        candidate_proxy(proxy, version, source)
        env["GOPROXY"] = proxy.as_uri() + "," + upstream
        env["GONOSUMDB"] = MODULE
        print(
            f"Rehearsing candidate {version}; this does not verify remote tags",
            flush=True,
        )
    for directory in publishable(source):
        name = module_path(directory)
        consumer = work / "consumers" / directory.replace("/", "-").replace(".", "root")
        consumer.mkdir(parents=True)
        env["GOMODCACHE"] = str(work / "module-caches" / consumer.name)
        (consumer / "go.mod").write_text("module release-consumer\n\ngo 1.25.2\n")
        (consumer / "main.go").write_text(
            f'package main\nimport _ "{name}"\nfunc main() {{}}\n'
        )
        run("go", "get", name + "@" + version, cwd=consumer, env=env)
        run("go", "build", "-mod=readonly", "./...", cwd=consumer, env=env)
        # Also compile packages not reached by the module's root import.
        run("go", "build", "-mod=readonly", name + "/...", cwd=consumer, env=env)
        resolved = json.loads(
            run("go", "list", "-m", "-json", name, cwd=consumer, env=env)
        )
        if resolved.get("Replace") or resolved.get("Version") != version:
            raise RuntimeError(
                f"{name}: consumer resolved an unexpected version or replacement"
            )
        print(f"  consumer build ok: {name}@{version}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "version", nargs="?", help="defaults to release-version in HEAD"
    )
    parser.add_argument(
        "--published",
        action="store_true",
        help="verify real published versions, not candidate zips",
    )
    args = parser.parse_args()
    try:
        snapshot = CommittedSource(ROOT)
        with tempfile.TemporaryDirectory(prefix="as-cache-release-") as temporary:
            work = Path(temporary)
            source = snapshot.export(work / "source")
            version = args.version or (source / "release-version").read_text().strip()
            if not re.fullmatch(r"v0\.[0-9]+\.[0-9]+", version):
                parser.error("expected a pre-1.0 release version such as v0.4.0")
            print(
                f"Validating committed source {snapshot.commit}; working-tree edits are excluded",
                flush=True,
            )
            preflight(version, source)
            check_consumers(work, version, args.published, source)
            count = len(publishable(source))
    except (RuntimeError, ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        return 1
    kind = "Published" if args.published else "Candidate"
    print(f"{kind} consumer checks passed for {count} modules.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
