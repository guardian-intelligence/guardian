import json
import fnmatch
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from classify_images import TARGETS, WEB_IMAGES, changed_paths, classify, main


class ClassifyImagesTest(unittest.TestCase):
    def names(self, paths, **kwargs):
        return {image["name"] for image in classify(paths, **kwargs)}

    def test_root_web_inputs_rebuild_all_web_images(self):
        for path in ["pnpm-lock.yaml", "pnpm-workspace.yaml", "package.json",
                     "patches/dependency.patch", "tsconfig.base.json", "tsconfig.json",
                     "vite.config.ts", ".npmrc", ".changeset/config.json",
                     ".changeset/security.md", "scripts/check-release-hygiene.ts"]:
            with self.subTest(path=path):
                self.assertEqual(self.names([path]), WEB_IMAGES)

    def test_tanstack_security_merge_diff_publishes_web_images(self):
        self.assertEqual(self.names(["pnpm-lock.yaml", "pnpm-workspace.yaml"]), WEB_IMAGES)

    def test_go_dependencies_rebuild_go_images(self):
        self.assertEqual(self.names(["go.mod", "go.sum"]), set(TARGETS) - WEB_IMAGES)

    def test_publisher_inputs_and_missing_base_rebuild_everything(self):
        for path in ["MODULE.bazel", "MODULE.bazel.lock", ".bazelrc", ".bazelversion",
                     ".github/workflows/images.yml", ".github/scripts/classify_images.py"]:
            with self.subTest(path=path):
                self.assertEqual(self.names([path]), set(TARGETS))
        self.assertEqual(self.names([], all_images=True), set(TARGETS))

    def test_service_and_shared_changes_preserve_selection(self):
        self.assertEqual(self.names(["src/anveio/web/src/routes/index.tsx"]), {"anveio-web"})
        self.assertEqual(self.names(["src/shared/ts/brand/index.ts"]),
                         {"anveio-web", "company-site", "privatecut-web"})
        self.assertEqual(self.names(["src/shared/ts/telemetry/index.ts"]),
                         {"company-site", "privatecut-web", "wake-up-mythra-web"})

    def test_promotion_and_documentation_do_not_rebuild_images(self):
        self.assertEqual(classify(["src/infrastructure/deployments/anveio/prod/web.yaml",
                                   "docs/dependency-management.md"]), [])

    def test_dispatch_and_new_branch_select_all(self):
        self.assertIsNone(changed_paths("workflow_dispatch", {}))
        self.assertIsNone(changed_paths("push", {"before": "0" * 40}))

    @patch("classify_images.subprocess.run")
    def test_unavailable_base_selects_all(self, exists):
        exists.return_value = subprocess.CompletedProcess([], 1)
        self.assertIsNone(changed_paths("push", {"before": "a" * 40}))

    def test_workflow_runs_classifier_for_root_web_inputs(self):
        workflow = (Path(__file__).resolve().parents[2] / ".github/workflows/images.yml").read_text()
        self.assertIn("run: python3 .github/scripts/classify_images.py", workflow)
        for path in ["pnpm-lock.yaml", "pnpm-workspace.yaml", "package.json", "patches/**",
                     "tsconfig.base.json", "tsconfig.json", "vite.config.ts", ".npmrc",
                     ".changeset/**", "scripts/**"]:
            with self.subTest(path=path):
                self.assertIn(f'      - "{path}"', workflow)

    def test_service_inputs_also_trigger_the_workflow(self):
        workflow = (Path(__file__).resolve().parents[2] / ".github/workflows/images.yml").read_text()
        triggers = [line.split('"')[1] for line in workflow.splitlines() if line.startswith('      - "')]
        for path in ["src/alerting/relay/main.go", "src/analytics/ingest/main.go",
                     "src/authorization/main.go", "src/chunkies/gateway/gateway.go",
                     "src/games/wake-up-mythra/client/game.ts", "src/payments/main.go",
                     "src/postflight/controlplane/main.go", "src/flags/notify/main.go",
                     "src/infrastructure/cmd/tofu_runner/main.go", "src/company/web/index.ts",
                     "src/privatecut/web/index.ts", "src/anveio/cal/index.ts",
                     "src/anveio/web/index.ts", "src/shared/ts/brand/index.ts",
                     "src/proto/guardian/authorization/v1/auth.proto"]:
            with self.subTest(path=path):
                self.assertTrue(self.names([path]))
                self.assertTrue(any(fnmatch.fnmatchcase(path, pattern) for pattern in triggers))

    @patch("classify_images.subprocess.run")
    @patch("classify_images.subprocess.check_output")
    def test_push_diff(self, diff, exists):
        exists.return_value = subprocess.CompletedProcess([], 0)
        diff.return_value = "pnpm-lock.yaml\npnpm-workspace.yaml\n"
        self.assertEqual(changed_paths("push", {"before": "a" * 40}),
                         ["pnpm-lock.yaml", "pnpm-workspace.yaml"])
        diff.assert_called_once_with(["git", "diff", "--name-only", "a" * 40, "HEAD"], text=True)

    def test_workflow_output_contract(self):
        with tempfile.TemporaryDirectory() as tmp:
            event = Path(tmp) / "event.json"
            output = Path(tmp) / "output"
            event.write_text(json.dumps({"before": "0" * 40}))
            with patch.dict("os.environ", GITHUB_EVENT_NAME="push",
                            GITHUB_EVENT_PATH=str(event), GITHUB_OUTPUT=str(output)):
                main()
            images = json.loads(output.read_text().removeprefix("images="))
            self.assertEqual({image["name"] for image in images}, set(TARGETS))
            self.assertTrue(all(image["target"] == TARGETS[image["name"]] for image in images))


if __name__ == "__main__":
    unittest.main()
