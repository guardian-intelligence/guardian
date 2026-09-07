import copy
from pathlib import Path
import tempfile
import unittest

from dependency_snapshot import MANIFESTS, normalize


class SnapshotTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.snapshot = {
            "version": 0,
            "scanned": "2026-09-07T06:00:00Z",
            "detector": {"name": "syft", "version": "1.51.1", "url": "https://github.com/anchore/syft"},
            "manifests": {
                str(self.root / name): {
                    "name": str(self.root / name),
                    "file": {"source_location": str(self.root / name)},
                    "resolved": {
                        "parent": {"package_url": "pkg:npm/parent@1.0.0", "dependencies": [""]},
                        "child": {"package_url": "pkg:npm/child@2.0.0", "dependencies": ["parent"], "relationship": "direct"},
                    },
                }
                for name in MANIFESTS
            },
        }

    def convert(self, snapshot=None, **kwargs):
        return normalize(snapshot or self.snapshot, self.root, kwargs.get("sha", "a" * 40), kwargs.get("ref", "refs/heads/main"), "123/1")

    def test_rebinds_paths_and_corrects_dependency_of_graph_without_mutating_input(self):
        original = copy.deepcopy(self.snapshot)
        result = self.convert()
        self.assertEqual(set(result["manifests"]), MANIFESTS)
        self.assertEqual(result["sha"], "a" * 40)
        self.assertEqual(result["job"], {"correlator": "guardian-images-dependencies", "id": "123/1"})
        for name, manifest in result["manifests"].items():
            self.assertEqual(manifest["file"]["source_location"], name)
            self.assertEqual(manifest["resolved"]["parent"]["dependencies"], ["child"])
            self.assertEqual(manifest["resolved"]["child"]["dependencies"], [])
            self.assertNotIn("relationship", manifest["resolved"]["child"])
        self.assertEqual(self.snapshot, original)

    def test_incomplete_scans_cannot_replace_graph(self):
        del self.snapshot["manifests"][str(self.root / "go.mod")]
        with self.assertRaisesRegex(ValueError, "omitted"):
            self.convert()

    def test_empty_manifest_cannot_replace_graph(self):
        self.snapshot["manifests"][str(self.root / "go.mod")]["resolved"] = {}
        with self.assertRaisesRegex(ValueError, "empty"):
            self.convert()

    def test_rejects_path_escape_and_dangling_edges(self):
        manifest = self.snapshot["manifests"][str(self.root / "go.mod")]
        manifest["file"]["source_location"] = str(self.root / "../go.mod")
        with self.assertRaisesRegex(ValueError, "escapes"):
            self.convert()
        manifest["file"]["source_location"] = str(self.root / "go.mod")
        del manifest["resolved"]["parent"]
        with self.assertRaisesRegex(ValueError, "edges"):
            self.convert()

    def test_rejects_untrusted_ref_and_noncommit_sha(self):
        with self.assertRaisesRegex(ValueError, "main"):
            self.convert(ref="refs/pull/1/merge")
        with self.assertRaisesRegex(ValueError, "commit"):
            self.convert(sha="main")


if __name__ == "__main__":
    unittest.main()
