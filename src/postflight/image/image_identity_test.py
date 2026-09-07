import copy
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import image_identity as identity


HERE = Path(__file__).resolve().parent


class ImageIdentityTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        for name in identity.RECIPE_PATHS:
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(name + "\n")
        self.host = self.repo / "src/postflight/host/host.py"
        self.host.parent.mkdir(parents=True)
        self.host.write_text("host v1\n")
        self.git("init", "-q")
        self.commit()
        self.artifacts = []
        for name in ("guestd", "listener", "base.qcow2"):
            path = self.root / name
            path.write_bytes(name.encode())
            self.artifacts.append(path)

    def git(self, *args):
        return subprocess.run(["git", "-C", str(self.repo), "-c", "core.hooksPath=/dev/null",
                               "-c", "user.name=Image Test", "-c", "user.email=image@example.invalid", *args],
                              check=True, text=True, capture_output=True).stdout.strip()

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")

    def describe(self, flavor="turbo"):
        return identity.image_identity(self.repo, *self.artifacts, flavor)

    def test_host_only_commits_and_dirty_unrelated_files_reuse_guest_identity(self):
        before = self.describe()
        self.host.write_text("host v2\n")
        self.commit()
        (self.repo / "unrelated.txt").write_text("untracked unrelated change\n")
        after = self.describe()
        self.assertNotEqual(before["source"]["commit"], after["source"]["commit"])
        self.assertEqual(before["image_id"], after["image_id"])
        self.assertEqual(before["inputs"], after["inputs"])
        self.assertEqual(after["source"]["dirty_recipes"], [])

    def test_every_recipe_and_artifact_byte_and_flavor_affect_identity(self):
        before = self.describe()
        for path in [*(self.repo / name for name in identity.RECIPE_PATHS), *self.artifacts]:
            with self.subTest(path=path.name):
                original = path.read_bytes()
                path.write_bytes(original + b"changed input\n")
                changed = self.describe()
                self.assertNotEqual(before["image_id"], changed["image_id"])
                self.assertEqual(before["source"]["commit"], changed["source"]["commit"])
                if path.is_relative_to(self.repo):
                    self.assertTrue(changed["source"]["dirty_recipes"])
                path.write_bytes(original)
        self.assertNotEqual(before["image_id"], self.describe("confidential")["image_id"])
        recipe = self.repo / identity.RECIPE_PATHS[0]
        recipe.chmod(0o755)
        self.assertNotEqual(before["image_id"], self.describe()["image_id"])

    def test_receipt_retains_original_provenance_and_rejects_replacement(self):
        before = self.describe()
        receipt_path = self.root / "receipts/image.json"
        snapshot = "pool/postflight/images/" + before["image_id"] + "@golden"
        with patch.object(identity, "snapshot_guid", return_value="123"):
            identity.publish_receipt(before, receipt_path, snapshot)
        receipt = identity.private_json(receipt_path)
        self.host.write_text("host v2\n")
        self.commit()
        identity.verify_receipt(self.describe(), receipt, snapshot, "123")
        self.assertEqual(receipt["source"], before["source"])
        for bad in (dict(receipt, snapshot_guid="124"), dict(receipt, input_sha256="0" * 64),
                    dict(receipt, inputs={}), dict(receipt, source={})):
            with self.subTest(bad=bad):
                with self.assertRaises(ValueError):
                    identity.verify_receipt(self.describe(), bad, snapshot, "123")
        receipt_path.chmod(0o644)
        with self.assertRaises(ValueError):
            identity.private_json(receipt_path)

    def test_actual_builder_cache_hit_requires_bound_receipt(self):
        expected = self.describe()
        identity_file = self.root / "identity.json"
        identity_file.write_text(json.dumps(expected))
        identity_file.chmod(0o600)
        receipt_path = self.root / "receipts/image.json"
        dataset = "pool/postflight/images/" + expected["image_id"]
        with patch.object(identity, "snapshot_guid", return_value="123"):
            identity.publish_receipt(expected, receipt_path, dataset + "@golden")
        original_receipt = identity.private_json(receipt_path)
        binary_dir = self.root / "bin"
        binary_dir.mkdir()
        zfs = binary_dir / "zfs"
        zfs.write_text('#!/bin/sh\ncase "$1" in\nlist) exit 0;;\nget) echo "$TEST_GUID";;\n*) exit 99;;\nesac\n')
        zfs.chmod(0o755)
        source = (HERE / "build.sh").read_text()
        start = source.index('if zfs list -H -o name "${dataset}@golden"')
        phase = source[start:source.index('fetch "${runner_url}"', start)]
        env = dict(os.environ, PATH=str(binary_dir) + os.pathsep + os.environ["PATH"],
                   TEST_GUID="123", script_dir=str(HERE), identity_file=str(identity_file),
                   receipt=str(receipt_path), dataset=dataset, image_id=expected["image_id"])

        def run():
            return subprocess.run(["bash", "-c", 'set -euo pipefail\nlog() { echo "$*" >&2; }\n' + phase],
                                  env=env, text=True, capture_output=True, timeout=10)

        result = run()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, expected["image_id"] + "\n")
        env["TEST_GUID"] = "124"
        self.assertNotEqual(run().returncode, 0)
        env["TEST_GUID"] = "123"
        unbound = copy.deepcopy(original_receipt)
        del unbound["input_sha256"]
        receipt_path.write_text(json.dumps(unbound))
        result = run()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        receipt_path.unlink()
        self.assertNotEqual(run().returncode, 0)

    def test_upstream_cache_key_tracks_bootstrap_and_adapter_recipes(self):
        source = (HERE / "build-upstream.sh").read_text()
        start = source.index('cache_input_sha256="$(')
        phase = source[start:source.index('cache_key=', start)]
        script_dir = self.repo / "src/postflight/image"
        env = dict(os.environ, script_dir=str(script_dir),
                   **{name: "fixture" for name in re.findall(r'\$\{([A-Z0-9_]+)\}', phase)})

        def key():
            # macOS's Bazel test PATH has BSD tools; production uses GNU
            # sha256sum on Linux. Keep real hashing in both environments.
            portable_hash = 'if ! command -v sha256sum >/dev/null; then sha256sum() { shasum -a 256 "$@"; }; fi\n'
            result = subprocess.run(["bash", "-c", "set -euo pipefail\n" + portable_hash + phase +
                                     '\nprintf "%s\\n" "${cache_input_sha256}"'],
                                    env=env, text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            return result.stdout.strip()

        original = key()
        self.host.write_text("unrelated host update")
        self.assertEqual(original, key())
        for name in ("build-upstream.sh", "render-qemu-template.py"):
            path = script_dir / name
            before = path.read_text()
            path.write_text(before + "recipe change\n")
            self.assertNotEqual(original, key(), name)
            path.write_text(before)


if __name__ == "__main__":
    unittest.main()
