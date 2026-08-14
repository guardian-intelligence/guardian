import importlib.machinery
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest


def load_workspace_watch():
    runfiles = os.environ.get("TEST_SRCDIR")
    workspace = os.environ.get("TEST_WORKSPACE")
    if runfiles and workspace:
        path = Path(runfiles) / workspace / "tools/ops/workspace-watch"
    else:
        path = Path(__file__).with_name("workspace-watch")
    loader = importlib.machinery.SourceFileLoader("workspace_watch", str(path))
    spec = importlib.util.spec_from_loader(loader.name, loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


workspace_watch = load_workspace_watch()

PORCELAIN = """worktree /repo
HEAD 1111111111111111111111111111111111111111
branch refs/heads/main

worktree /repo/.claude/worktrees/engine
HEAD 2222222222222222222222222222222222222222
branch refs/heads/feature
locked claude session engine (pid 42)

worktree /tmp/audit
HEAD 3333333333333333333333333333333333333333
detached
"""


def worktree(path="/tmp/wt", branch="feature", head="4" * 40, **extra):
    record = {"worktree": path, "HEAD": head}
    if branch:
        record["branch"] = branch
    record.update(extra)
    return record


def classify(wt, merged_by="PR #7 merged", unmerged_reason=None, dirty=0, idle_seconds=99999):
    return workspace_watch.classify_worktree(
        wt,
        primary="/repo",
        merged_by=merged_by,
        unmerged_reason=unmerged_reason,
        dirty=dirty,
        idle_seconds=idle_seconds,
        idle_minimum=120 * 60,
    )


class ParseWorktreesTest(unittest.TestCase):
    def test_parses_branch_lock_and_detached_records(self):
        parsed = workspace_watch.parse_worktrees(PORCELAIN)
        self.assertEqual(
            [(w["worktree"], w.get("branch"), w.get("locked"), w.get("detached")) for w in parsed],
            [
                ("/repo", "main", None, None),
                ("/repo/.claude/worktrees/engine", "feature", "claude session engine (pid 42)", None),
                ("/tmp/audit", None, None, True),
            ],
        )


class PrimarySyncTest(unittest.TestCase):
    def test_clean_main_behind_fast_forwards(self):
        self.assertEqual(
            workspace_watch.plan_primary_sync("main", dirty=0, ahead=0, behind=3),
            ("fast-forward", "3 commit(s) behind origin/main"),
        )

    def test_clean_main_at_tip_is_current(self):
        action, _ = workspace_watch.plan_primary_sync("main", dirty=0, ahead=0, behind=0)
        self.assertEqual(action, "current")

    def test_uncommitted_work_is_left_alone(self):
        self.assertEqual(
            workspace_watch.plan_primary_sync("main", dirty=12, ahead=0, behind=3),
            ("skipped", "12 uncommitted change(s)"),
        )

    def test_local_commits_are_left_alone(self):
        self.assertEqual(
            workspace_watch.plan_primary_sync("main", dirty=0, ahead=2, behind=3),
            ("skipped", "2 local commit(s) not on origin/main"),
        )

    def test_other_branches_are_left_alone(self):
        self.assertEqual(
            workspace_watch.plan_primary_sync("wum-auth", dirty=0, ahead=0, behind=3),
            ("skipped", "on branch wum-auth, not main"),
        )


class ClassifyWorktreeTest(unittest.TestCase):
    def test_merged_clean_idle_worktree_is_removed(self):
        self.assertEqual(classify(worktree()), ("remove", "PR #7 merged"))

    def test_primary_workspace_is_never_removed(self):
        self.assertEqual(classify(worktree(path="/repo")), ("keep", "primary workspace"))

    def test_locked_worktree_keeps_its_lock_reason(self):
        self.assertEqual(
            classify(worktree(locked="claude session engine (pid 42)")),
            ("keep", "locked: claude session engine (pid 42)"),
        )

    def test_unmerged_worktree_is_kept(self):
        self.assertEqual(
            classify(worktree(), merged_by=None),
            ("keep", "not merged into origin/main"),
        )

    def test_branch_that_moved_after_its_merge_keeps_that_reason(self):
        self.assertEqual(
            classify(worktree(), merged_by=None, unmerged_reason="commits after PR #7 merged"),
            ("keep", "commits after PR #7 merged"),
        )

    def test_uncommitted_work_survives_even_when_merged(self):
        self.assertEqual(classify(worktree(), dirty=3), ("keep", "3 uncommitted change(s)"))

    def test_recently_touched_worktree_survives_even_when_merged(self):
        self.assertEqual(classify(worktree(), idle_seconds=300), ("keep", "active 5m ago"))


class MergeTimelineTest(unittest.TestCase):
    def test_branch_at_merge_time_is_settled(self):
        self.assertFalse(
            workspace_watch.commits_after_merge("2026-08-01T10:00:00+00:00", "2026-08-01T10:05:00Z")
        )

    def test_commit_after_the_merge_is_unshipped_work(self):
        self.assertTrue(
            workspace_watch.commits_after_merge("2026-08-01T11:00:00+00:00", "2026-08-01T10:05:00Z")
        )

    def test_unknown_timestamps_count_as_unshipped_work(self):
        self.assertTrue(workspace_watch.commits_after_merge("", "2026-08-01T10:05:00Z"))
        self.assertTrue(workspace_watch.commits_after_merge("2026-08-01T10:00:00+00:00", None))


class AccessVerdictTest(unittest.TestCase):
    def test_working_probe_with_fresh_token_is_healthy(self):
        self.assertEqual(
            workspace_watch.auth_verdict(exec_ok=True, probe_ok=True, repaired=False, age_days=2.0),
            ("healthy", False),
        )

    def test_token_near_its_idle_window_is_stale(self):
        self.assertEqual(
            workspace_watch.auth_verdict(exec_ok=True, probe_ok=True, repaired=False, age_days=27.0),
            ("stale", False),
        )

    def test_successful_remint_needs_no_human(self):
        self.assertEqual(
            workspace_watch.auth_verdict(exec_ok=False, probe_ok=True, repaired=True, age_days=1.0),
            ("repaired", False),
        )

    def test_failed_remint_pages_for_a_device_login(self):
        self.assertEqual(
            workspace_watch.auth_verdict(exec_ok=False, probe_ok=False, repaired=False, age_days=None),
            ("needs-device-login", True),
        )


class CredentialPluginTest(unittest.TestCase):
    def test_executable_plugin_reports_its_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            plugin = Path(tmp) / "kubectl-oidc_login"
            plugin.write_text("#!/bin/sh\n")
            plugin.chmod(0o755)
            self.assertEqual(
                workspace_watch.exec_binary_healthy({"command": str(plugin)}),
                (True, str(plugin)),
            )

    def test_plugin_deleted_with_its_worktree_is_unhealthy(self):
        with tempfile.TemporaryDirectory() as tmp:
            missing = str(Path(tmp) / "gone" / "kubectl-oidc_login")
            healthy, detail = workspace_watch.exec_binary_healthy({"command": missing})
            self.assertFalse(healthy)
            self.assertEqual(detail, f"credential plugin missing: {missing}")

    def test_context_without_an_exec_credential_is_unhealthy(self):
        self.assertEqual(
            workspace_watch.exec_binary_healthy(None),
            (False, "no exec credential in the current context"),
        )


class NotifyTest(unittest.TestCase):
    def test_entering_the_bad_state_pages(self):
        self.assertTrue(workspace_watch.should_notify({"access_status": "healthy"}, "needs-device-login", 1000.0))

    def test_healthy_passes_stay_quiet(self):
        self.assertFalse(workspace_watch.should_notify({"access_status": "needs-device-login"}, "healthy", 1000.0))

    def test_a_withheld_privacy_grant_pages(self):
        self.assertTrue(workspace_watch.should_notify({"access_status": "healthy"}, "blocked", 1000.0))

    def test_repeat_pages_wait_for_the_cooldown(self):
        state = {"access_status": "needs-device-login", "notified_at": 1000.0}
        self.assertFalse(workspace_watch.should_notify(state, "needs-device-login", 2000.0, cooldown=3600))
        self.assertTrue(workspace_watch.should_notify(state, "needs-device-login", 5000.0, cooldown=3600))


class TokenAgeTest(unittest.TestCase):
    def test_age_tracks_the_newest_non_lock_cache_entry(self):
        with tempfile.TemporaryDirectory() as tmp:
            cache = Path(tmp)
            (cache / "token").write_text("{}")
            os.utime(cache / "token", (0, 86400 * 3))
            (cache / "token.lock").write_text("")
            os.utime(cache / "token.lock", (0, 86400 * 10))
            self.assertEqual(workspace_watch.token_age_days(cache, 86400 * 5), 2.0)

    def test_missing_cache_has_no_age(self):
        self.assertIsNone(workspace_watch.token_age_days("/nonexistent/oidc-cache", 0))


if __name__ == "__main__":
    unittest.main()
