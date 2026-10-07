"""Routine tidy uses tracked modules and tolerates unpublished sibling versions."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent


class TidyTests(unittest.TestCase):
    def test_tracked_modules_exclude_ignored_clones_and_skip_unpublished(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            scripts = root / "scripts"
            scripts.mkdir()
            for name in ("tidy.py", "release_check.py", "committed_source.py"):
                shutil.copyfile(ROOT / "scripts" / name, scripts / name)
            subprocess.run(["git", "init", "-q", str(root)], check=True)
            modules = (".", "leaf", "unpublished")
            for module in (*modules, ".reports/clone"):
                directory = root / module
                directory.mkdir(parents=True, exist_ok=True)
                (directory / "go.mod").write_text("module fixture\n")
            (root / ".gitignore").write_text(".reports/\n")
            subprocess.run(
                ["git", "add", ".gitignore", *[f"{m}/go.mod" for m in modules]],
                cwd=root,
                check=True,
            )
            binary = root / "bin"
            binary.mkdir()
            fake_go = binary / "go"
            fake_go.write_text(
                "#!/usr/bin/env python3\n"
                "import json, os, pathlib, sys\n"
                "assert os.environ['GOWORK'] == 'off'\n"
                "args = sys.argv[1:]\n"
                "if args == ['mod', 'edit', '-json']:\n"
                "    deps = [{'Path': 'github.com/sshaplygin/as-cache', "
                "'Version': 'v9.9.9'}] if pathlib.Path.cwd().name == 'unpublished' else []\n"
                "    print(json.dumps({'Require': deps}))\n"
                "elif args[:2] == ['list', '-m']:\n"
                "    sys.exit(1)\n"
                "elif args == ['mod', 'tidy']:\n"
                "    pathlib.Path('.tidied').write_text('yes')\n"
                "else:\n"
                "    sys.exit('unexpected arguments: ' + repr(args))\n"
            )
            fake_go.chmod(0o755)
            result = subprocess.run(
                [os.sys.executable, str(scripts / "tidy.py")],
                cwd=root,
                env=dict(
                    os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"]
                ),
                capture_output=True,
                text=True,
            )
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            self.assertTrue((root / ".tidied").is_file())
            self.assertTrue((root / "leaf/.tidied").is_file())
            self.assertFalse((root / "unpublished/.tidied").exists())
            self.assertFalse((root / ".reports/clone/.tidied").exists())
            self.assertIn(
                "SKIP tidy unpublished: unresolved sibling versions:", result.stdout
            )


if __name__ == "__main__":
    unittest.main()
