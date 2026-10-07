"""Binary format checked against the pinned reader's 24-byte layout."""

from pathlib import Path
import struct
import tempfile
import unittest

from oracle_trace import write_oracle


class OracleTraceTest(unittest.TestCase):
    def test_identity_size_and_next_access_survive_export(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "trace.bin"
            result = write_oracle([("a", 3), ("b", 7), ("a", 4)], path)
            self.assertEqual(
                [(0, 1, 3, 3), (1, 2, 7, -1), (2, 1, 4, -1)],
                list(struct.iter_unpack("<IQIq", path.read_bytes())),
            )
            self.assertEqual(3, result["requests"])
            self.assertEqual(2, result["distinct_keys"])
            self.assertEqual(5, result["mean_first_object_bytes"])
