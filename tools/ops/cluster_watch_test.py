import datetime
import importlib.machinery
import importlib.util
import os
from pathlib import Path
import unittest


def load_cluster_watch():
    runfiles = os.environ.get("TEST_SRCDIR")
    workspace = os.environ.get("TEST_WORKSPACE")
    if runfiles and workspace:
        path = Path(runfiles) / workspace / "tools/ops/cluster-watch"
    else:
        path = Path(__file__).with_name("cluster-watch")
    loader = importlib.machinery.SourceFileLoader("cluster_watch", str(path))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


cluster_watch = load_cluster_watch()
NOW = datetime.datetime(2026, 7, 13, 12, tzinfo=datetime.timezone.utc)


def condition(condition_type, status, reason="Reconciled"):
    return {
        "type": condition_type,
        "status": status,
        "reason": reason,
        "message": "",
    }


def resource(name, namespace="test", **values):
    obj = {"metadata": {"name": name, "namespace": namespace}}
    obj.update(values)
    return obj


def fake_get(objects):
    def get_json(args):
        return {"items": objects.get(args[1], [])}
    return get_json


class ClusterWatchStatusTest(unittest.TestCase):
    def test_idle_kargo_stage_and_converged_rollouts_are_healthy(self):
        objects = {
            "kustomizations.kustomize.toolkit.fluxcd.io": [resource(
                "guardian-system",
                spec={"sourceRef": {"name": "guardian"}},
                status={
                    "conditions": [condition("Ready", "True")],
                    "lastAppliedRevision": "main@sha1:abc123",
                },
            )],
            "helmreleases.helm.toolkit.fluxcd.io": [resource(
                "flagger", status={"conditions": [condition("Ready", "True")]},
            )],
            "stages.kargo.akuity.io": [resource(
                "prod", status={
                    "conditions": [condition("Healthy", "Unknown", "NoFreight")],
                },
            )],
            "warehouses.kargo.akuity.io": [resource(
                "company-site", status={"conditions": [
                    condition("Ready", "True"),
                    condition("Healthy", "True"),
                ]},
            )],
            "promotions.kargo.akuity.io": [resource(
                "prod.completed",
                spec={"stage": "prod"},
                status={"phase": "Succeeded"},
            )],
            "canaries.flagger.app": [resource(
                "company-site", status={"phase": "Succeeded"},
            )],
        }
        objects["promotions.kargo.akuity.io"][0]["metadata"]["creationTimestamp"] = (
            "2026-07-13T11:00:00Z"
        )

        findings = cluster_watch.collect_status(
            "abc123", NOW, fake_get(objects),
        )

        self.assertEqual([], findings)

    def test_revision_promotion_and_canary_block_convergence(self):
        objects = {
            "kustomizations.kustomize.toolkit.fluxcd.io": [resource(
                "guardian-system",
                spec={"sourceRef": {"name": "guardian"}},
                status={
                    "conditions": [condition("Ready", "True")],
                    "lastAppliedRevision": "main@sha1:old",
                },
            )],
            "helmreleases.helm.toolkit.fluxcd.io": [],
            "stages.kargo.akuity.io": [],
            "warehouses.kargo.akuity.io": [],
            "promotions.kargo.akuity.io": [resource(
                "prod.running",
                spec={"stage": "prod"},
                status={"phase": "Running"},
            )],
            "canaries.flagger.app": [resource(
                "company-site", status={"phase": "Failed"},
            )],
        }
        objects["promotions.kargo.akuity.io"][0]["metadata"]["creationTimestamp"] = (
            "2026-07-13T11:30:00Z"
        )

        findings = cluster_watch.collect_status(
            "new", NOW, fake_get(objects),
        )

        self.assertEqual(
            {"RevisionPending", "Running", "Failed"},
            {item["reason"] for item in findings},
        )

    def test_old_failed_promotion_does_not_block_forever(self):
        promotion = resource(
            "prod.failed",
            spec={"stage": "prod"},
            status={"phase": "Failed"},
        )
        promotion["metadata"]["creationTimestamp"] = "2026-07-13T01:00:00Z"
        objects = {
            "kustomizations.kustomize.toolkit.fluxcd.io": [],
            "helmreleases.helm.toolkit.fluxcd.io": [],
            "stages.kargo.akuity.io": [],
            "warehouses.kargo.akuity.io": [],
            "promotions.kargo.akuity.io": [promotion],
            "canaries.flagger.app": [],
        }

        findings = cluster_watch.collect_status("", NOW, fake_get(objects))

        self.assertEqual([], findings)


if __name__ == "__main__":
    unittest.main()
