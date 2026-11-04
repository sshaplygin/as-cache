"""The published evidence archive must verify only when it is the pinned dataset."""

import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import unittest
import urllib.error
from unittest.mock import patch

from evidence_archive import (
    ARCHIVE_ENV,
    POINTER,
    archive_bytes,
    package,
    sha256,
    verify,
)
from record_evidence import combine
from record_evidence_test import batch_fixture, complete_manifest, write_batches
from trace_inputs import digest


def build(root, release="v0.0.1"):
    """A recorded dataset, packaged: returns (dataset, archive, results)."""
    dataset = root / "dataset"
    dataset.mkdir()
    complete_manifest(dataset)
    archive = root / f"as-cache-evidence-{release}.tar.gz"
    results = root / "results"
    package(dataset, release, archive, results)
    return dataset, archive, results


def repoint(results, data):
    pointer = json.loads((results / POINTER).read_text())
    pointer["sha256"] = sha256(data)
    (results / POINTER).write_text(json.dumps(pointer))


def tar_with(members):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
        for name, data in members.items():
            info = tarfile.TarInfo(name)
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
    return buffer.getvalue()


def tar_raw(entries):
    """Build a tar.gz from (TarInfo-kwargs, data) pairs, allowing duplicate
    names and non-regular members."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w:gz") as archive:
        for fields, data in entries:
            info = tarfile.TarInfo(fields["name"])
            for key, value in fields.items():
                setattr(info, key, value)
            info.size = len(data) if info.type == tarfile.REGTYPE else 0
            archive.addfile(info, io.BytesIO(data) if data else None)
    return buffer.getvalue()


class _Scratch:
    """A TemporaryDirectory stand-in rooted inside the test directory, so a
    member that escaped its extraction directory would land where the test can
    see it."""

    def __init__(self, root):
        self.path = root / "scratch" / "extract"

    def __enter__(self):
        shutil.rmtree(self.path.parent, ignore_errors=True)
        self.path.mkdir(parents=True)
        return str(self.path)

    def __exit__(self, *exc):
        shutil.rmtree(self.path.parent, ignore_errors=True)
        return False


class EvidenceArchiveTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.dataset, self.archive, self.results = build(self.root)
        self.env = patch.dict(os.environ, {ARCHIVE_ENV: str(self.archive)})
        self.env.start()

    def tearDown(self):
        self.env.stop()
        self.temporary.cleanup()

    def test_packaging_is_deterministic_and_keeps_no_data_in_the_repository(self):
        self.assertEqual(archive_bytes(self.dataset), archive_bytes(self.dataset))
        self.assertEqual(
            {"README.md", POINTER}, {p.name for p in self.results.iterdir()}
        )
        with tarfile.open(self.archive) as archive:
            names = set(archive.getnames())
        self.assertNotIn("README.md", names)
        self.assertNotIn("traces.json", names)
        self.assertIn("traces-1.json", names)
        verify(self.results)

    def test_changed_archive_bytes_fail_the_pinned_hash(self):
        self.archive.write_bytes(self.archive.read_bytes() + b"\0")
        with self.assertRaisesRegex(ValueError, "sha256"):
            verify(self.results)

    def test_unexpected_or_missing_members_fail_even_with_a_matching_hash(self):
        with tarfile.open(self.archive) as archive:
            members = {m.name: archive.extractfile(m).read() for m in archive}
        cases = {
            # Hostile names come first so nothing is written before they are refused.
            "extra": ({"notes.txt": b"x", **members}, "unexpected archive member"),
            "traversal": ({"../escape": b"x", **members}, "unexpected archive member"),
            "missing": (
                {k: v for k, v in members.items() if k != "bytes.log"},
                "missing artifacts",
            ),
        }
        for description, (contents, message) in cases.items():
            with self.subTest(description):
                data = tar_with(contents)
                self.archive.write_bytes(data)
                repoint(self.results, data)
                with patch(
                    "tempfile.TemporaryDirectory", return_value=_Scratch(self.root)
                ):
                    with self.assertRaisesRegex(ValueError, message):
                        verify(self.results)
                self.assertFalse(
                    (self.root / "scratch" / "escape").exists(), "a member escaped"
                )

    def test_edited_repository_report_fails(self):
        readme = self.results / "README.md"
        readme.write_text(readme.read_text() + "Unsupported conclusion.\n")
        with self.assertRaisesRegex(ValueError, "README differs"):
            verify(self.results)

    def test_measurement_data_left_in_the_repository_fails(self):
        shutil.copyfile(self.dataset / "traces.json", self.results / "traces.json")
        with self.assertRaisesRegex(ValueError, "must hold only"):
            verify(self.results)

    def test_report_generator_changes_do_not_invalidate_a_published_archive(self):
        manifest = json.loads((self.dataset / "manifest.json").read_text())
        manifest["artifacts_sha256"]["README.md"] = "0" * 64
        (self.dataset / "manifest.json").write_text(json.dumps(manifest))
        data = archive_bytes(self.dataset)
        self.archive.write_bytes(data)
        repoint(self.results, data)
        verify(self.results)

    def test_tampered_raw_batch_fails_even_with_its_own_hash_updated(self):
        batch = self.dataset / "traces-1.json"
        data = json.loads(batch.read_text())
        data["traces"][0]["adaptive"][0]["hit_rate_percent"]["runs"][0] = 99
        batch.write_text(json.dumps(data))
        manifest = json.loads((self.dataset / "manifest.json").read_text())
        manifest["artifacts_sha256"]["traces-1.json"] = digest(batch)
        (self.dataset / "manifest.json").write_text(json.dumps(manifest))
        archive = archive_bytes(self.dataset)
        self.archive.write_bytes(archive)
        repoint(self.results, archive)
        with self.assertRaisesRegex(ValueError, "traces.json"):
            verify(self.results)

    def test_measured_commit_must_match_the_archive(self):
        pointer = json.loads((self.results / POINTER).read_text())
        pointer["measured_commit"] = "c" * 40
        (self.results / POINTER).write_text(json.dumps(pointer))
        with self.assertRaisesRegex(ValueError, "measured_commit"):
            verify(self.results)

    def test_unreachable_asset_fails_loudly_without_a_local_copy(self):
        self.env.stop()
        try:
            with patch.dict(os.environ, {}, clear=False):
                os.environ.pop(ARCHIVE_ENV, None)
                with patch(
                    "urllib.request.urlopen",
                    side_effect=urllib.error.URLError("offline"),
                ):
                    with self.assertRaisesRegex(ValueError, ARCHIVE_ENV):
                        verify(self.results)
        finally:
            self.env.start()

    def test_duplicate_and_non_regular_members_are_refused(self):
        with tarfile.open(self.archive) as archive:
            members = [(m.name, archive.extractfile(m).read()) for m in archive]
        regular = [({"name": name}, data) for name, data in members]
        cases = {
            "duplicate": (
                regular + [({"name": "bytes.log"}, b"second copy")],
                "duplicate archive member",
            ),
            "symlink": (
                [e for e in regular if e[0]["name"] != "bytes.log"]
                + [
                    (
                        {
                            "name": "bytes.log",
                            "type": tarfile.SYMTYPE,
                            "linkname": "/etc/passwd",
                        },
                        b"",
                    )
                ],
                "unexpected archive member",
            ),
            "hard link": (
                [e for e in regular if e[0]["name"] != "bytes.log"]
                + [
                    (
                        {
                            "name": "bytes.log",
                            "type": tarfile.LNKTYPE,
                            "linkname": "reference.log",
                        },
                        b"",
                    )
                ],
                "unexpected archive member",
            ),
        }
        for description, (entries, message) in cases.items():
            with self.subTest(description):
                data = tar_raw(entries)
                self.archive.write_bytes(data)
                repoint(self.results, data)
                with self.assertRaisesRegex(ValueError, message):
                    verify(self.results)

    def test_pointer_must_name_exactly_the_release_asset(self):
        good = json.loads((self.results / POINTER).read_text())
        asset = good["asset"]
        cases = {
            "other release in url": dict(
                good,
                url=f"https://github.com/sshaplygin/as-cache/releases/download/v9.9.9/{asset}",
            ),
            "other host": dict(good, url=f"https://example.com/{asset}"),
            "asset of another release": dict(
                good,
                asset="as-cache-evidence-v9.9.9.tar.gz",
                url="https://github.com/sshaplygin/as-cache/releases/download/v0.0.1/as-cache-evidence-v9.9.9.tar.gz",
            ),
        }
        for description, pointer in cases.items():
            with self.subTest(description):
                (self.results / POINTER).write_text(json.dumps(pointer))
                with self.assertRaisesRegex(ValueError, "does not match its release"):
                    verify(self.results)

    def test_packaging_refuses_an_invalid_dataset(self):
        # A complete dataset whose manifest records a failed command: every file
        # is present, so only validation can stop it from being packed.
        manifest = json.loads((self.dataset / "manifest.json").read_text())
        manifest["commands"][0]["exit_code"] = 1
        (self.dataset / "manifest.json").write_text(json.dumps(manifest))
        archive = self.root / "invalid.tar.gz"
        with self.assertRaisesRegex(ValueError, "measurement command failed"):
            package(self.dataset, "v0.0.2", archive, self.root / "r2")
        self.assertFalse(archive.exists())

    def test_ignored_dotfiles_do_not_count_as_repository_data(self):
        (self.results / ".DS_Store").write_bytes(b"finder")
        verify(self.results)

    def test_pooled_output_bytes_are_pinned_for_published_assets(self):
        # Published assets carry the hashes of the pooled files that verification
        # rebuilds with combine(). Changing its byte output - indentation, key
        # order, trailing newline - makes every attached asset unverifiable, and
        # assets are never replaced. Change these constants only together with a
        # new measurement for every release that must stay verifiable.
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            write_batches(directory, [batch_fixture() for _ in range(3)])
            combine(directory, "a" * 40)
            self.assertEqual(
                "3359b6ecdd6445c5a41aed98b35c0b8550e9bc8a0684658ae453fdd3e1f03127",
                sha256((directory / "traces.json").read_bytes()),
            )
            self.assertEqual(
                "9e75c468501cdd4d2fb80402a6344fb7709ee22ec7d6f65eca64d6485f9edf38",
                sha256((directory / "tuning.json").read_bytes()),
            )

    def test_make_gate_is_read_only_and_rejects_tampering(self):
        root = self.root / "repository"
        repository = Path(__file__).resolve().parent.parent
        root.mkdir()
        shutil.copyfile(repository / "Makefile", root / "Makefile")
        shutil.copytree(
            repository / "scripts",
            root / "scripts",
            ignore=shutil.ignore_patterns("__pycache__"),
        )
        shutil.copytree(self.results, root / "bench/results/current")
        directory = root / "bench/results/current"
        before = {p.name: p.read_bytes() for p in directory.iterdir()}
        env = dict(os.environ, **{ARCHIVE_ENV: str(self.archive)})
        result = subprocess.run(
            ["make", "evidence-check"],
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
        )
        self.assertEqual(0, result.returncode, result.stdout + result.stderr)
        self.assertEqual(before, {p.name: p.read_bytes() for p in directory.iterdir()})
        readme = directory / "README.md"
        readme.write_text(readme.read_text() + "Unsupported conclusion.\n")
        result = subprocess.run(
            ["make", "evidence-check"],
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(0, result.returncode)
        self.assertIn("README differs from the report", result.stderr)


if __name__ == "__main__":
    unittest.main()
