"""Reject incomplete or inconsistent retained evidence, even with valid hashes."""

import json
from pathlib import Path
import tempfile
import unittest

from record_evidence import verify_manifest


class EvidenceManifestTest(unittest.TestCase):
    def test_empty_inventory_cannot_certify_unmeasured_data(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name in ("traces.json", "tuning.json", "bytes.json"):
                (directory / name).write_text(json.dumps({"commit": "unmeasured"}))
            (directory / "manifest.json").write_text(
                json.dumps(
                    {"commit": "unmeasured", "artifacts_sha256": {}, "commands": []}
                )
            )
            with self.assertRaisesRegex(ValueError, "required artifacts"):
                verify_manifest(directory)


if __name__ == "__main__":
    unittest.main()
