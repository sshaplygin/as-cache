"""Packaging regressions exercised against isolated miniature repositories."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile
import importlib.util

ROOT = Path(__file__).resolve().parent.parent
MODULE = "github.com/sshaplygin/as-cache"
MODULES = (".", "lfu", "policies", "policies/arc", "policies/tinylfu", "metrics", "bandit", "benchclient")


class ReleaseCheckTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="as-cache-release-test-")
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / "scripts").mkdir()
        for name in ("release-check.sh", "release_check.py"):
            if (ROOT / "scripts" / name).exists():
                shutil.copyfile(ROOT / "scripts" / name, self.root / "scripts" / name)
        (self.root / "release-version").write_text("v0.4.0\n")
        # fifo is present in source but deliberately outside the release set.
        for name in (*MODULES, "policies/fifo"):
            p = self.root / name
            p.mkdir(parents=True, exist_ok=True)
            module = MODULE if name == "." else f"{MODULE}/{name}"
            (p / "go.mod").write_text(f"module {module}\n\ngo 1.25.2\n")
            (p / "LICENSE").write_text("Test fixture license\n")
            (p / "cache.go").write_text("package cache\n\nconst Value = 1\n")
        subprocess.run(["git", "init", "-q"], cwd=self.root, check=True)
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "user.name", "Fixture")
        self.commit()

    def git(self, *args):
        subprocess.run(["git", *args], cwd=self.root, check=True,
                       stdout=subprocess.DEVNULL)

    def commit(self):
        self.git("add", ".")
        self.git("commit", "--allow-empty", "-qm", "fixture")

    def check_release(self, stage=True):
        if stage:
            self.commit()
        env = dict(os.environ, GOWORK="off", GOPROXY="off", GOSUMDB="off")
        return subprocess.run(
            ["bash", "scripts/release-check.sh", "v0.4.0"],
            cwd=self.root, env=env, text=True, stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT, timeout=180,
        )

    def test_candidate_builds_without_workspace_or_external_proxy(self):
        result = self.check_release()
        self.assertEqual(0, result.returncode, result.stdout)

    def test_rejects_license_present_only_outside_git_index(self):
        subprocess.run(["git", "add", "."], cwd=self.root, check=True)
        subprocess.run(["git", "rm", "--cached", "benchclient/LICENSE"],
                       cwd=self.root, check=True, stdout=subprocess.DEVNULL)
        self.git("commit", "-qm", "omit license")
        result = self.check_release(stage=False)
        self.assertNotEqual(0, result.returncode, result.stdout)
        self.assertIn("missing tracked nonempty LICENSE", result.stdout)

    def test_rejects_local_replace_even_with_real_looking_version(self):
        p = self.root / "benchclient/go.mod"
        p.write_text(p.read_text() + f"\nrequire {MODULE} v0.4.0\nreplace {MODULE} => ..\n")
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)
        self.assertIn('published go.mod must not contain replace', result.stdout)

    def test_rejects_sibling_version_outside_candidate(self):
        p = self.root / "benchclient/go.mod"
        p.write_text(p.read_text() + f"\nrequire {MODULE} v0.9.99\n")
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)
        self.assertIn('must use release version', result.stdout)

    def test_rejects_unpublished_fifo_dependency(self):
        p = self.root / "benchclient/go.mod"
        p.write_text(p.read_text() + f"\nrequire {MODULE}/policies/fifo v0.4.0\n")
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)
        self.assertIn('outside the release set', result.stdout)

    def test_rejects_code_that_only_builds_with_local_api(self):
        p = self.root / "benchclient/go.mod"
        p.write_text(p.read_text() + f"\nrequire {MODULE} v0.4.0\n")
        (p.parent / "cache.go").write_text(
            f'package cache\nimport core "{MODULE}"\nvar Value = core.MissingSymbol\n'
        )
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)
        self.assertIn('undefined: core.MissingSymbol', result.stdout)

    def test_rejects_uncommitted_tracked_edits(self):
        path = self.root / "policies/cache.go"
        path.write_text(path.read_text() + "var UncommittedProbe = 1\n")
        result = self.check_release(stage=False)
        self.assertNotEqual(0, result.returncode, result.stdout)
        self.assertIn("commit tracked changes", result.stdout)

    def test_rejects_staged_new_files(self):
        (self.root / "policies/new.go").write_text("package cache\n")
        self.git("add", ".")
        result = self.check_release(stage=False)
        self.assertNotEqual(0, result.returncode, result.stdout)
        self.assertIn("commit tracked changes", result.stdout)

    def test_discovers_new_module_without_allowlist_edit(self):
        path = self.root / "policies/new"
        path.mkdir()
        (path / "go.mod").write_text(f"module {MODULE}/policies/new\n\ngo 1.25.2\n")
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)
        self.assertIn("policies/new: missing tracked nonempty LICENSE", result.stdout)

    def test_nested_modules_are_excluded_from_parent_zip(self):
        spec = importlib.util.spec_from_file_location(
            "checker", self.root / "scripts/release_check.py")
        checker = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(checker)
        proxy = self.root / "proxy"
        checker.candidate_proxy(proxy, "v0.4.0")
        for module in (MODULE, MODULE + "/policies"):
            with zipfile.ZipFile(proxy / module / "@v/v0.4.0.zip") as archive:
                names = [name.removeprefix(module + "@v0.4.0/")
                         for name in archive.namelist()]
            self.assertIn("go.mod", names)
            self.assertNotIn("fifo/go.mod", names)
            self.assertNotIn("policies/fifo/go.mod", names)
            self.assertEqual(["go.mod"], [n for n in names if n.endswith("go.mod")])


if __name__ == "__main__":
    unittest.main()
