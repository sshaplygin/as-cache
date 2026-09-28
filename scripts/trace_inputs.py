"""Verify trace files against the repository's pinned sizes and SHA-256 hashes."""

import argparse
import hashlib
import json
from pathlib import Path

CATALOG = Path(__file__).with_name("trace-inputs.json")


def digest(path):
    with path.open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def verify(path, entry):
    if not path.is_file() or path.stat().st_size != entry["bytes"]:
        raise ValueError(
            f"{entry['file']}: missing file or size mismatch (expected {entry['bytes']})"
        )
    if digest(path) != entry["sha256"]:
        raise ValueError(f"{entry['file']}: SHA-256 mismatch")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("directory", type=Path)
    parser.add_argument("name", nargs="?")
    parser.add_argument("--path", type=Path, help="staged download to verify as name")
    args = parser.parse_args()
    entries = json.loads(CATALOG.read_text())["files"]
    if args.name:
        entries = [entry for entry in entries if entry["file"] == args.name]
        if not entries:
            parser.error(f"unpinned input: {args.name}")
    try:
        for entry in entries:
            verify(args.path or args.directory / entry["file"], entry)
    except ValueError as error:
        parser.exit(1, f"FAIL: {error}\n")


if __name__ == "__main__":
    main()
