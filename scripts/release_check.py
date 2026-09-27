#!/usr/bin/env python3
"""Build release candidates as consumers, without repository replacements.

Candidate mode serves tracked source as module zips from a temporary file proxy.
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

MODULE = "github.com/sshaplygin/as-cache"
# FIFO stays experimental; it must not enter any published dependency graph.
PUBLISHABLE = (".", "lfu", "policies", "policies/arc", "policies/tinylfu", "metrics", "bandit", "benchclient")
ROOT = Path(__file__).resolve().parent.parent


def run(*args, cwd=ROOT, env=None):
    result = subprocess.run(args, cwd=cwd, env=env, text=True,
                            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if result.returncode:
        raise RuntimeError(f"{' '.join(args)} failed ({result.returncode}):\n{result.stdout}")
    return result.stdout


def module_path(directory):
    return MODULE if directory == "." else f"{MODULE}/{directory}"


def preflight(version):
    expected = {module_path(d) for d in PUBLISHABLE}
    env = dict(os.environ, GOWORK="off", GOFLAGS="")
    for directory in PUBLISHABLE:
        path = ROOT / directory
        metadata = json.loads(run("go", "mod", "edit", "-json", cwd=path, env=env))
        if metadata["Module"]["Path"] != module_path(directory):
            raise RuntimeError(f"{directory}: unexpected module path")
        if not any(p.is_file() and p.stat().st_size for p in path.glob("LICENSE*")):
            raise RuntimeError(f"{directory}: missing nonempty LICENSE")
        if metadata.get("Replace"):
            raise RuntimeError(f"{directory}: published go.mod must not contain replace directives")
        for dependency in metadata.get("Require") or []:
            name = dependency["Path"]
            if name != MODULE and not name.startswith(MODULE + "/"):
                continue
            if name not in expected:
                raise RuntimeError(f"{directory}: {name} is outside the release set")
            if dependency["Version"] != version:
                raise RuntimeError(f"{directory}: {name}@{dependency['Version']} must use release version {version}")
        print(f"  metadata ok: {directory}", flush=True)


def candidate_proxy(destination, version):
    # Only tracked files are packaged. Nested modules must never leak into the
    # parent zip, including modules deliberately omitted from this release.
    names = run("git", "ls-files", "-z").split("\0")
    tracked = [Path(name) for name in names if name]
    module_dirs = {p.parent for p in tracked if p.name == "go.mod"}
    for directory in PUBLISHABLE:
        base = Path(directory)
        name = module_path(directory)
        target = destination / name / "@v"
        target.mkdir(parents=True)
        (target / f"{version}.mod").write_bytes((ROOT / base / "go.mod").read_bytes())
        info = {"Version": version, "Time": datetime.now(timezone.utc).isoformat()}
        (target / f"{version}.info").write_text(json.dumps(info))
        (target / "list").write_text(version + "\n")
        with zipfile.ZipFile(target / f"{version}.zip", "w", zipfile.ZIP_DEFLATED) as archive:
            for file in tracked:
                if base != Path(".") and base not in file.parents:
                    continue
                if any(child != base and child in file.parents
                       and (base == Path(".") or base in child.parents)
                       for child in module_dirs):
                    continue
                source = ROOT / file
                if source.is_symlink():
                    raise RuntimeError(f"{file}: symlinks are not supported in release candidates")
                if not source.is_file():
                    raise RuntimeError(f"{file}: tracked release input is missing")
                archive.write(source, f"{name}@{version}/{file.relative_to(base)}")


def check_consumers(work, version, published):
    # A fresh module cache prevents candidate versions from contaminating a
    # developer's real cache or a prior download from hiding a missing version.
    env = dict(os.environ, GOWORK="off", GOFLAGS="", GOMODCACHE=str(work / "modules"),
               GOPRIVATE="", GONOPROXY="none")
    upstream = os.environ.get("GOPROXY", "https://proxy.golang.org,direct")
    if published:
        env["GOPROXY"] = upstream
        env["GONOSUMDB"] = ""
        print(f"Checking published {version} through {upstream}", flush=True)
    else:
        proxy = work / "proxy"
        candidate_proxy(proxy, version)
        env["GOPROXY"] = proxy.as_uri() + "," + upstream
        env["GONOSUMDB"] = MODULE
        print(f"Rehearsing candidate {version}; this does not verify remote tags", flush=True)
    for directory in PUBLISHABLE:
        name = module_path(directory)
        consumer = work / "consumers" / directory.replace("/", "-").replace(".", "root")
        consumer.mkdir(parents=True)
        env["GOMODCACHE"] = str(work / "module-caches" / consumer.name)
        (consumer / "go.mod").write_text("module release-consumer\n\ngo 1.25.2\n")
        (consumer / "main.go").write_text(f'package main\nimport _ "{name}"\nfunc main() {{}}\n')
        run("go", "get", name + "@" + version, cwd=consumer, env=env)
        run("go", "build", "-mod=readonly", "./...", cwd=consumer, env=env)
        # Also compile packages not reached by the module's root import.
        run("go", "build", "-mod=readonly", name + "/...", cwd=consumer, env=env)
        resolved = json.loads(run("go", "list", "-m", "-json", name, cwd=consumer, env=env))
        if resolved.get("Replace") or resolved.get("Version") != version:
            raise RuntimeError(f"{name}: consumer resolved an unexpected version or replacement")
        print(f"  consumer build ok: {name}@{version}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("version", nargs="?", default=(ROOT / "release-version").read_text().strip())
    parser.add_argument("--published", action="store_true", help="verify real published versions, not candidate zips")
    args = parser.parse_args()
    if not re.fullmatch(r"v0\.[0-9]+\.[0-9]+", args.version):
        parser.error("expected a pre-1.0 release version such as v0.4.0")
    try:
        preflight(args.version)
        with tempfile.TemporaryDirectory(prefix="as-cache-release-") as temporary:
            check_consumers(Path(temporary), args.version, args.published)
    except (RuntimeError, OSError) as error:
        print(f"FAIL: {error}", file=sys.stderr)
        return 1
    kind = "Published" if args.published else "Candidate"
    print(f"{kind} consumer checks passed for {len(PUBLISHABLE)} modules.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
