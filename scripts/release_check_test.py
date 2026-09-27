"""Packaging regressions exercised against isolated miniature repositories."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

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

    def check_release(self, stage=True):
        if stage:
            subprocess.run(["git", "add", "."], cwd=self.root, check=True)
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
        result = self.check_release(stage=False)
        self.assertNotEqual(0, result.returncode, result.stdout)

    def test_rejects_local_replace_even_with_real_looking_version(self):
        p = self.root / "benchclient/go.mod"
        p.write_text(p.read_text() + f"\nrequire {MODULE} v0.4.0\nreplace {MODULE} => ..\n")
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)

    def test_rejects_sibling_version_outside_candidate(self):
        p = self.root / "benchclient/go.mod"
        p.write_text(p.read_text() + f"\nrequire {MODULE} v0.9.99\n")
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)

    def test_rejects_unpublished_fifo_dependency(self):
        p = self.root / "benchclient/go.mod"
        p.write_text(p.read_text() + f"\nrequire {MODULE}/policies/fifo v0.4.0\n")
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)

    def test_rejects_code_that_only_builds_with_local_api(self):
        p = self.root / "benchclient/go.mod"
        p.write_text(p.read_text() + f"\nrequire {MODULE} v0.4.0\n")
        (p.parent / "cache.go").write_text(
            f'package cache\nimport core "{MODULE}"\nvar Value = core.MissingSymbol\n'
        )
        result = self.check_release()
        self.assertNotEqual(0, result.returncode, result.stdout)


if __name__ == "__main__":
    unittest.main()
