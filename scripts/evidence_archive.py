"""Publish the current evidence dataset as a release asset and verify it.

The repository keeps only the generated results README and a pointer to the
release asset that holds the measurements. The asset contains the raw batches,
logs, reference and byte results and the measurement manifest; the pooled
traces.json and tuning.json are rebuilt from the raw batches, and the README is
rendered by the current generator, so a wording change in the report never
invalidates a published archive.
"""

import argparse
from contextlib import contextmanager
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import re
import tarfile
import tempfile
import urllib.error
import urllib.request

from record_evidence import REQUIRED_ARTIFACTS, combine, verify_inputs
from render_evidence import render, report_text

POINTER = "release-asset.json"
DERIVED = {"README.md", "traces.json", "tuning.json"}
ARCHIVED = (REQUIRED_ARTIFACTS - DERIVED) | {"manifest.json"}
REPOSITORY_FILES = {"README.md", POINTER}
ARCHIVE_ENV = "AS_CACHE_EVIDENCE_ARCHIVE"
POINTER_KEYS = {"release", "asset", "url", "sha256", "measured_commit"}


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def archive_bytes(dataset):
    """Pack the archived artifacts deterministically: sorted names, no
    timestamps or owners, so the same dataset always yields the same bytes."""
    buffer = io.BytesIO()
    with gzip.GzipFile(
        fileobj=buffer, mode="wb", mtime=0, filename="", compresslevel=9
    ) as compressed:
        with tarfile.open(
            fileobj=compressed, mode="w", format=tarfile.USTAR_FORMAT
        ) as archive:
            for name in sorted(ARCHIVED):
                data = (dataset / name).read_bytes()
                info = tarfile.TarInfo(name)
                info.size = len(data)
                info.mode = 0o644
                info.mtime = 0
                archive.addfile(info, io.BytesIO(data))
    return buffer.getvalue()


def unpack(data, directory):
    """Extract only the expected regular files, by name, into directory."""
    seen = set()
    with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as archive:
        for member in archive.getmembers():
            if not member.isreg() or member.name not in ARCHIVED:
                raise ValueError(f"unexpected archive member: {member.name}")
            if member.name in seen:
                raise ValueError(f"duplicate archive member: {member.name}")
            seen.add(member.name)
            (directory / member.name).write_bytes(archive.extractfile(member).read())
    if seen != ARCHIVED:
        raise ValueError(
            "archive is missing artifacts: " + ", ".join(sorted(ARCHIVED - seen))
        )


def asset_name(release):
    return f"as-cache-evidence-{release}.tar.gz"


def asset_url(release):
    return (
        "https://github.com/sshaplygin/as-cache/releases/download/"
        f"{release}/{asset_name(release)}"
    )


def read_pointer(results):
    pointer = json.loads((results / POINTER).read_text())
    if set(pointer) != POINTER_KEYS:
        raise ValueError(f"{POINTER} must contain exactly {sorted(POINTER_KEYS)}")
    if not re.fullmatch(r"[0-9a-f]{64}", pointer["sha256"]):
        raise ValueError(f"{POINTER} has no valid sha256")
    if not re.fullmatch(r"[0-9a-f]{40}", pointer["measured_commit"]):
        raise ValueError(f"{POINTER} has no valid measured_commit")
    release = pointer["release"]
    if not re.fullmatch(r"v\d+\.\d+\.\d+", release) or (
        pointer["asset"] != asset_name(release) or pointer["url"] != asset_url(release)
    ):
        raise ValueError(
            f"{POINTER} asset or url does not match its release {release!r}"
        )
    return pointer


def fetch(pointer):
    """Read the archive from AS_CACHE_EVIDENCE_ARCHIVE when set, else download
    it. Either way it must match the pinned sha256; there is no skip."""
    local = os.environ.get(ARCHIVE_ENV)
    if local:
        data = Path(local).read_bytes()
    else:
        try:
            with urllib.request.urlopen(pointer["url"], timeout=60) as response:
                data = response.read()
        except (urllib.error.URLError, TimeoutError) as error:
            raise ValueError(
                f"cannot download {pointer['url']}: {error}; "
                f"set {ARCHIVE_ENV} to a local copy to verify offline"
            ) from error
    if sha256(data) != pointer["sha256"]:
        raise ValueError(
            f"{pointer['asset']} sha256 {sha256(data)} differs from {POINTER}"
        )
    return data


@contextmanager
def published_dataset(results):
    """Yield the published dataset, unpacked, pooled, rendered and validated."""
    pointer = read_pointer(results)
    data = fetch(pointer)
    with tempfile.TemporaryDirectory(prefix="as-cache-evidence-") as temporary:
        dataset = Path(temporary)
        unpack(data, dataset)
        manifest = json.loads((dataset / "manifest.json").read_text())
        if manifest.get("commit") != pointer["measured_commit"]:
            raise ValueError("archive manifest commit differs from measured_commit")
        combine(dataset, manifest["commit"])
        render(dataset)
        verify_inputs(dataset, check_report=False)
        yield pointer, dataset


def verify(results):
    # Hidden files such as .DS_Store are ignored by Git and are not data.
    actual = {p.name for p in results.iterdir() if not p.name.startswith(".")}
    if actual != REPOSITORY_FILES:
        raise ValueError(
            f"{results} must hold only {sorted(REPOSITORY_FILES)}; "
            "measurement data belongs in the release asset"
        )
    with published_dataset(results) as (pointer, dataset):
        if (results / "README.md").read_bytes() != (dataset / "README.md").read_bytes():
            raise ValueError(
                "README differs from the report generated from the published dataset; "
                "run scripts/evidence_archive.py --render"
            )
    print(
        f"Verified {pointer['asset']} ({pointer['sha256'][:12]}) measured at "
        f"{pointer['measured_commit']}; README regenerated byte for byte"
    )


def render_repository(results):
    with published_dataset(results) as (_, dataset):
        (results / "README.md").write_bytes((dataset / "README.md").read_bytes())
    print(f"Rendered {results / 'README.md'} from the published dataset")


def package(dataset, release, archive_path, results):
    """Pack a validated recording and point the repository at it. The archive
    must then be attached to the named release under its asset name."""
    if not re.fullmatch(r"v\d+\.\d+\.\d+", release):
        raise ValueError(f"release must be a version tag, got {release!r}")
    manifest = verify_inputs(dataset, check_report=False)
    data = archive_bytes(dataset)
    asset = asset_name(release)
    archive_path.write_bytes(data)
    pointer = {
        "release": release,
        "asset": asset,
        "url": asset_url(release),
        "sha256": sha256(data),
        "measured_commit": manifest["commit"],
    }
    results.mkdir(parents=True, exist_ok=True)
    for stale in results.iterdir():
        if stale.name not in REPOSITORY_FILES and not stale.name.startswith("."):
            raise ValueError(
                f"remove {stale} first: {results} keeps no measurement data"
            )
    (results / POINTER).write_text(json.dumps(pointer, indent=2) + "\n")
    # verify_inputs proved the pooled files equal their raw batches, so this is
    # the report verify() regenerates from the archive.
    (results / "README.md").write_bytes(report_text(dataset).encode("utf-8"))
    print(f"Wrote {archive_path} and {results / POINTER}")
    print(f"Attach it: gh release upload {release} {archive_path}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    choice = parser.add_mutually_exclusive_group(required=True)
    choice.add_argument("--verify", type=Path, metavar="RESULTS")
    choice.add_argument("--render", type=Path, metavar="RESULTS")
    choice.add_argument("--package", type=Path, metavar="DATASET")
    parser.add_argument("--release", help="version tag the asset is attached to")
    parser.add_argument("--archive", type=Path, help="where to write the asset")
    parser.add_argument("--results", type=Path, default=Path("bench/results/current"))
    args = parser.parse_args()
    try:
        if args.verify:
            verify(args.verify)
        elif args.render:
            render_repository(args.render)
        else:
            if not args.release or not args.archive:
                raise ValueError("--package needs --release and --archive")
            package(args.package, args.release, args.archive, args.results)
    except (ValueError, KeyError, OSError, tarfile.TarError) as error:
        parser.exit(1, f"FAIL: {error}\n")


if __name__ == "__main__":
    main()
