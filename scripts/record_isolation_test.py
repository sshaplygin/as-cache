"""Recording must execute committed inputs without touching the developer tree."""

import json
from contextlib import nullcontext
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import record_evidence


class RecordingIsolationTest(unittest.TestCase):
    def setUp(self):
        self.work = tempfile.TemporaryDirectory()
        self.addCleanup(self.work.cleanup)
        self.root = Path(self.work.name).resolve() / "repo"
        self.root.mkdir()
        self.git("init", "-q")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        (self.root / "scripts").mkdir()
        (self.root / "scripts/record_evidence.py").write_text(
            "import json, os, subprocess\n"
            "from pathlib import Path\n"
            "def measure(out):\n"
            "    out.mkdir(parents=True, exist_ok=True)\n"
            '    output = subprocess.check_output(["make", "--silent", "run"], text=True)\n'
            '    state = subprocess.check_output(["git", "status", "--porcelain"], text=True)\n'
            '    commit = subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip()\n'
            '    (out / "observed.json").write_text(json.dumps(dict(output=output, state=state, commit=commit, probe=Path("injected_test.go").exists(), workspace=os.environ.get("GOWORK"))))\n'
        )
        (self.root / "Makefile").write_text("run:\n\tgo run .\n")
        (self.root / "go.mod").write_text("module fixture\n\ngo 1.25.2\n")
        (self.root / "go.work").write_text("go 1.25.2\nuse .\n")
        (self.root / "main.go").write_text(
            'package main\nimport ("embed"; "fmt")\n'
            "//go:embed _explicit.txt all:data\nvar files embed.FS\n"
            'func main() { a,_:=files.ReadFile("_explicit.txt"); b,_:=files.ReadFile("data/_hidden.txt"); fmt.Printf("%s/%s",a,b) }\n'
        )
        (self.root / "_explicit.txt").write_text("committed")
        (self.root / "data").mkdir()
        (self.root / "data/_hidden.txt").write_text("hidden committed")
        (self.root / ".gitignore").write_text("output/\n*.txt\ninjected_test.go\n")
        self.git("add", "-f", ".")
        self.git("commit", "-qm", "fixture")
        self.commit = self.git("rev-parse", "HEAD").strip()

    def git(self, *args):
        return subprocess.check_output(["git", *args], cwd=self.root, text=True)

    def test_actual_snapshot_execution_ignores_preserved_assets_and_disk_script(self):
        output = Path(self.work.name) / "results"
        for preserved in (False, True):
            with self.subTest(preserved=preserved):
                if preserved:
                    self.git("update-index", "--assume-unchanged", "_explicit.txt")
                    self.git("update-index", "--skip-worktree", "data/_hidden.txt")
                    (self.root / "_explicit.txt").write_text("uncommitted")
                    (self.root / "data/_hidden.txt").write_text("uncommitted hidden")
                    (self.root / "injected_test.go").write_text(
                        "preserved probe, invalid Go"
                    )
                    (self.root / "scripts/record_evidence.py").write_text(
                        "raise RuntimeError('disk script ran')\n"
                    )
                destination = self.root / "output" if preserved else output / "fresh"
                with (
                    patch.object(record_evidence, "ROOT", self.root),
                    patch.dict(
                        os.environ, {"AS_CACHE_TRACES": str(self.root / "traces")}
                    ),
                ):
                    record_evidence.record(destination)
                measured = json.loads((destination / "observed.json").read_text())
                self.assertEqual("committed/hidden committed", measured["output"])
                self.assertEqual(self.commit, measured["commit"])
                self.assertEqual("", measured["state"])
                self.assertFalse(measured["probe"])
        self.assertEqual(
            "preserved probe, invalid Go", (self.root / "injected_test.go").read_text()
        )
        self.assertEqual("uncommitted", (self.root / "_explicit.txt").read_text())

    def test_workspace_path_is_canonical_through_symlinked_temporary_parent(self):
        work = Path(self.work.name).resolve()
        physical = work / "physical"
        physical.mkdir()
        alias = work / "alias"
        alias.symlink_to(physical, target_is_directory=True)
        output = work / "results"
        with (
            patch.object(record_evidence, "ROOT", self.root),
            patch.dict(os.environ, {"AS_CACHE_TRACES": str(self.root / "traces")}),
            patch.object(
                record_evidence.tempfile,
                "TemporaryDirectory",
                return_value=nullcontext(str(alias)),
            ),
        ):
            record_evidence.record(output)
        measured = json.loads((output / "observed.json").read_text())
        self.assertEqual("committed/hidden committed", measured["output"])
        self.assertEqual(str(physical / "source/go.work"), measured["workspace"])

    def test_persisted_go_flags_cannot_overlay_committed_source(self):
        work = Path(self.work.name)
        replacement = work / "replacement.go"
        replacement.write_text(
            'package main\nimport "fmt"\nfunc main() { fmt.Print("OUTSIDE COMMITTED INPUTS") }\n'
        )
        overlay = work / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {"main.go": str(replacement)}}))
        goenv = work / "goenv"
        goenv.write_text(f"GOFLAGS=-overlay={overlay}\n")
        output = work / "results"
        with (
            patch.object(record_evidence, "ROOT", self.root),
            patch.dict(
                os.environ,
                {"AS_CACHE_TRACES": str(self.root / "traces"), "GOENV": str(goenv)},
            ),
        ):
            record_evidence.record(output)
        self.assertEqual(
            "committed/hidden committed",
            json.loads((output / "observed.json").read_text())["output"],
        )

    def test_external_makefile_cannot_reintroduce_go_overlay(self):
        work = Path(self.work.name)
        replacement = work / "replacement.go"
        replacement.write_text(
            'package main\nimport "fmt"\nfunc main() { fmt.Print("OUTSIDE COMMITTED INPUTS") }\n'
        )
        overlay = work / "overlay.json"
        overlay.write_text(json.dumps({"Replace": {"main.go": str(replacement)}}))
        makefile = work / "injected.mk"
        makefile.write_text(f"export GOFLAGS = -overlay={overlay}\n")
        output = work / "results"
        with (
            patch.object(record_evidence, "ROOT", self.root),
            patch.dict(
                os.environ,
                {
                    "AS_CACHE_TRACES": str(self.root / "traces"),
                    "MAKEFILES": str(makefile),
                },
            ),
        ):
            record_evidence.record(output)
        self.assertEqual(
            "committed/hidden committed",
            json.loads((output / "observed.json").read_text())["output"],
        )

    def test_inherited_git_index_cannot_modify_developer_staging(self):
        (self.root / "main.go").write_text(
            (self.root / "main.go").read_text() + "\n// staged developer edit\n"
        )
        self.git("add", "main.go")
        index = self.root / ".git/index"
        before = index.read_bytes()
        with (
            patch.object(record_evidence, "ROOT", self.root),
            patch.dict(
                os.environ,
                {
                    "AS_CACHE_TRACES": str(self.root / "traces"),
                    "GIT_INDEX_FILE": str(index),
                },
            ),
        ):
            record_evidence.record(Path(self.work.name) / "results")
        self.assertEqual(before, index.read_bytes())
        self.assertIn("staged developer edit", self.git("diff", "--cached"))

    def test_output_is_validated_before_starting_measurements(self):
        with patch.object(record_evidence, "ROOT", self.root):
            with patch.object(record_evidence, "CommittedSource") as snapshot:
                with self.assertRaisesRegex(ValueError, "must be ignored"):
                    record_evidence.record(self.root / "results")
                snapshot.assert_not_called()
                self.assertFalse((self.root / "results").exists())
            self.assertEqual(
                self.root / "output",
                record_evidence.validate_output(self.root / "output"),
            )
            external = Path(self.work.name).resolve() / "external"
            self.assertEqual(external, record_evidence.validate_output(external))
            external.mkdir()
            (external / "retain").write_text("do not overwrite")
            with self.assertRaisesRegex(ValueError, "must be empty"):
                record_evidence.validate_output(external)
            self.assertEqual("do not overwrite", (external / "retain").read_text())


if __name__ == "__main__":
    unittest.main()
