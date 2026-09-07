import copy
import ipaddress
import json
import os
from pathlib import Path
import shutil
import signal
import stat
import subprocess
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import host


HERE = Path(__file__).resolve().parent


class HostTest(unittest.TestCase):
    def setUp(self):
        self.manifest = host.load_manifest(HERE / "hosts/rust-forge-01.json")

    def test_turbo_contract_and_secret_separation(self):
        files = host.render(self.manifest, "noble-turbo-012345-gabcdef", "4.2")
        env = files["hostd.env"]
        for value in ('HOSTD_SLOTS="2"', 'HOSTD_CPUS="4"', 'HOSTD_MEMORY_MIB="16384"',
                      'HOSTD_POOL="postflight_ci/postflight"',
                      'HOSTD_FIRMWARE_PATH="/usr/share/seabios/bios.bin"',
                      'HOSTD_WARM_TEMPLATE_DIR="/var/lib/postflight/warm-templates"'):
            self.assertIn(value, env)
        self.assertNotIn("HOSTD_SYNC_SECRET", env)
        self.assertIn("EnvironmentFile=/etc/postflight/secrets.env", files["hostd-override.conf"])
        self.assertIn("Requires=postflight-storage.service", files["hostd-override.conf"])
        self.assertIn("Before=hostd.service", files["postflight-storage.service"])
        self.assertIn("OnUnitInactiveSec=5min", files["postflight-reconcile.timer"])

    def test_network_denies_private_and_metadata_before_egress(self):
        rules = host.nft_rules(self.manifest, ("inet", "bridge"))
        self.assertNotIn("flush ruleset", rules)
        self.assertEqual(rules.count("delete table"), 2)
        self.assertIn("delete table inet postflight_ci", rules)
        self.assertIn("delete table bridge postflight_ci", rules)
        self.assertLess(rules.index("ip daddr @blocked_ipv4 drop"), rules.index('iifname "pfbr0" accept'))
        blocked = [ipaddress.IPv4Network(cidr) for cidr in host.BLOCKED_IPV4]
        for value in ("0.1.2.3", "10.10.10.10", "100.100.100.200", "127.0.0.1", "169.254.169.254",
                      "172.16.0.1", "192.168.1.1", "192.88.99.1", "198.18.0.1", "224.0.0.1", "255.255.255.255"):
            self.assertTrue(any(ipaddress.IPv4Address(value) in subnet for subnet in blocked), value)
        for value in ("1.1.1.1", "8.8.8.8", "140.82.112.4"):
            self.assertFalse(any(ipaddress.IPv4Address(value) in subnet for subnet in blocked), value)
        self.assertIn('ip daddr 10.77.0.1 tcp dport { 53, 8480 } accept', rules)
        self.assertIn('iifname "pft*" oifname "pft*" drop', rules)
        self.assertIn('iifname "pfbr0" meta nfproto ipv6 drop', rules)
        self.assertIn('oifname "pfbr0" meta nfproto ipv6 drop', rules)
        self.assertIn('ip saddr 10.77.0.0/24 oifname != "pfbr0" masquerade', rules)

    def test_manifest_rejects_privileged_render_injection_and_confidential_image(self):
        for key, value in (("bridge", 'pfbr0"; accept'), ("dhcp_start", "192.168.0.32"),
                           ("dhcp_start", "10.77.0.1"), ("control_plane", "https://attacker.invalid"),
                           ("vdev_file", "/dev/sda"), ("firmware", "/usr/share/OVMF/OVMF_CODE_4M.fd"),
                           ("slots", True), ("cpus", 8), ("host_firewall", "none"), ("bridge", "pfbr1")):
            with self.subTest(key=key, value=value), tempfile.TemporaryDirectory() as temp:
                changed = copy.deepcopy(self.manifest)
                changed[key] = value
                path = Path(temp) / "host.json"
                path.write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    host.load_manifest(path)
        with self.assertRaises(ValueError):
            host.render(self.manifest, "noble-confidential-test", "4.2")

    def test_ufw_is_ipv4_scoped_to_dhcp_dns_checkout_and_guarded_forwarding(self):
        rules = host.ufw_rules(self.manifest)
        self.assertEqual(len(rules), 6)
        input_rules = [rule for rule in rules if rule[0] == "allow"]
        self.assertEqual(len(input_rules), 4)
        self.assertEqual({(rule[rule.index("proto") + 1], rule[-3]) for rule in input_rules},
                         {("udp", "67"), ("udp", "53"), ("tcp", "53"), ("tcp", "8480")})
        dhcp = input_rules[0]
        self.assertEqual(dhcp[dhcp.index("from") + 1:dhcp.index("to")], ("0.0.0.0/0", "port", "68"))
        for rule in rules:
            self.assertEqual(rule[rule.index("on") + 1], "pfbr0")
            self.assertNotIn("any", rule)  # An IPv4 wildcard must not expand into IPv6.
            self.assertTrue(rule[-1].startswith("postflight-ci-"))
        for rule in input_rules[1:]:
            self.assertEqual(rule[rule.index("from") + 1], "10.77.0.0/24")
            self.assertEqual(rule[rule.index("to") + 1], "10.77.0.1")
        nft = host.nft_rules(self.manifest)
        self.assertIn("hook forward priority -20", nft)
        nft_lines = [line.strip() for line in nft.splitlines()]
        self.assertLess(nft_lines.index('oifname "pfbr0" ct state established,related accept'),
                        nft_lines.index('oifname "pfbr0" drop'))
        self.assertEqual([rule[2] for rule in rules if rule[0] == "route"], ["in", "out"])

    def test_ufw_is_never_relaxed_before_nft_guard_commit(self):
        for failure in (None, "check", "apply"):
            calls = []

            def run(*args, **kwargs):
                calls.append(args)
                if args[:3] == ("ip", "-j", "-d"):
                    return SimpleNamespace(returncode=0, stdout='[{"linkinfo":{"info_kind":"bridge"}}]')
                if args[0] == "nft" and ((failure == "check" and "--check" in args) or
                                        (failure == "apply" and args[1] == "-f")):
                    raise subprocess.CalledProcessError(1, args)
                return SimpleNamespace(returncode=0, stdout="Status: active\n")

            with self.subTest(failure=failure), patch.object(host, "command", side_effect=run), \
                    patch.object(host.shutil, "which", return_value="/usr/sbin/ufw"):
                if failure:
                    with self.assertRaises(subprocess.CalledProcessError):
                        host.ensure_network(self.manifest)
                    self.assertFalse(any(call[0] == "ufw" for call in calls))
                else:
                    host.ensure_network(self.manifest)
                    first_ufw = next(i for i, call in enumerate(calls) if call[0] == "ufw")
                    self.assertLess(calls.index(("nft", "-f", "-")), first_ufw)
                    self.assertEqual([call[1:] for call in calls if call[0] == "ufw"],
                                     [("status",), *host.ufw_rules(self.manifest)])

    def test_ufw_preserves_inactive_or_missing_host_firewall(self):
        with patch.object(host.shutil, "which", return_value=None), patch.object(host, "command") as run:
            with self.assertRaisesRegex(ValueError, "missing provisioned ufw"):
                host.ensure_ufw(self.manifest)
            run.assert_not_called()
        with patch.object(host.shutil, "which", return_value="/usr/sbin/ufw"), \
                patch.object(host, "command", return_value=SimpleNamespace(stdout="Status: inactive\n")) as run:
            with self.assertRaisesRegex(ValueError, "not active"):
                host.ensure_ufw(self.manifest)
            self.assertEqual([call.args for call in run.call_args_list], [("ufw", "status")])

    def test_existing_unimportable_vdev_is_never_formatted(self):
        with tempfile.TemporaryDirectory() as temp:
            backing = Path(temp) / "pool.img"
            backing.write_bytes(b"existing blocks must survive")
            m = dict(self.manifest, vdev_file=str(backing))
            calls = []

            def run(*args, **kwargs):
                calls.append(args)
                if args[:2] == ("zpool", "list"):
                    return SimpleNamespace(returncode=1, stdout="")
                if args[:2] == ("zpool", "import"):
                    raise subprocess.CalledProcessError(1, args)
                self.fail("unexpected mutation of existing vdev: " + repr(args))

            with patch.object(host, "command", side_effect=run), \
                    patch.object(host, "secure_directory"), patch.object(host, "secret_file"), \
                    patch.object(Path, "lstat", return_value=SimpleNamespace(st_mode=stat.S_IFREG | 0o600, st_uid=0)), \
                    patch.object(host.os, "posix_fallocate", create=True) as allocate:
                with self.assertRaises(subprocess.CalledProcessError):
                    host.ensure_storage(m)
                allocate.assert_not_called()
            self.assertEqual(backing.read_bytes(), b"existing blocks must survive")
            self.assertFalse(any(call[:2] == ("zpool", "create") for call in calls))

    def test_imported_wrong_pool_is_not_modified(self):
        def run(*args, **kwargs):
            if args[:2] == ("zpool", "list"):
                return SimpleNamespace(returncode=0, stdout="postflight_ci")
            if args[:2] == ("zpool", "status"):
                return SimpleNamespace(returncode=0, stdout="/dev/sda ONLINE")
            self.fail("unexpected mutation of unrelated imported pool: " + repr(args))

        with patch.object(host, "command", side_effect=run), \
                patch.object(host, "secure_directory"), patch.object(host, "secret_file"):
            with self.assertRaisesRegex(ValueError, "declared backing file"):
                host.ensure_storage(self.manifest)

    def test_source_reconciler_is_syntax_valid_and_uses_protected_source(self):
        script = HERE / "reconcile.sh"
        subprocess.run(["bash", "-n", str(script)], check=True)
        text = script.read_text()
        self.assertIn("source_dir=/opt/postflight/source", text)
        self.assertIn("merge-base --is-ancestor", text)
        self.assertIn("git ls-tree -r HEAD --", text)
        self.assertNotIn("/home/ubuntu", text)
        self.assertNotIn("apt-get", text)
        self.assertNotIn("secrets.env", text)

    @unittest.skipUnless(shutil.which("flock"), "requires util-linux flock")
    def test_reconcile_lock_is_not_retained_by_orphan_build_child(self):
        text = (HERE / "reconcile.sh").read_text()
        # Execute the production lock/re-entry block with a disposable worker,
        # without privileged preflight, Git access, builds, or host mutation.
        lock_block = text[text.index("lock_file=/run/postflight-reconcile/reconcile.lock"):
                          text.index('if [[ ! -d "${source_dir}/.git" ]]')]
        for worker_exit in (0, 19):
            with self.subTest(worker_exit=worker_exit), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                lock = root / "reconcile.lock"
                pid_file, release, entered = (root / name for name in ("child.pid", "release", "entered"))
                script = root / "reconcile.sh"
                script.write_text('set -euo pipefail\nhost_id="fixture-host"\n' +
                                  lock_block.replace("/run/postflight-reconcile/reconcile.lock", str(lock)) + r'''
if [[ "${TEST_RECONCILE_MODE:-}" == probe ]]; then
  : >"${TEST_RECONCILE_ENTERED}"
  exit 23
fi
sleep 60 </dev/null >/dev/null 2>&1 &
printf '%s\n' "$!" >"${TEST_RECONCILE_CHILD_PID}"
while [[ ! -f "${TEST_RECONCILE_RELEASE}" ]]; do sleep 0.02; done
exit "${TEST_RECONCILE_EXIT}"
''')
                env = dict(os.environ, TEST_RECONCILE_CHILD_PID=str(pid_file),
                           TEST_RECONCILE_RELEASE=str(release), TEST_RECONCILE_ENTERED=str(entered),
                           TEST_RECONCILE_EXIT=str(worker_exit))
                worker = subprocess.Popen(["bash", str(script), "fixture-host"], env=env)
                child = None
                try:
                    deadline = time.monotonic() + 5
                    while not pid_file.exists() and time.monotonic() < deadline and worker.poll() is None:
                        time.sleep(0.02)
                    self.assertTrue(pid_file.exists(), "worker never entered the critical section")
                    child = int(pid_file.read_text())
                    probe_env = dict(env, TEST_RECONCILE_MODE="probe")
                    blocked = subprocess.run(["bash", str(script), "fixture-host"], env=probe_env, timeout=5)
                    self.assertEqual(blocked.returncode, 0, "contended reconcile must skip successfully")
                    self.assertFalse(entered.exists(), "concurrent reconcile entered the critical section")
                    release.touch()
                    self.assertEqual(worker.wait(timeout=5), worker_exit, "worker exit status was lost")
                    os.kill(child, 0)  # The original build child still lives.
                    next_run = subprocess.run(["bash", str(script), "fixture-host"], env=probe_env, timeout=5)
                    self.assertEqual(next_run.returncode, 23, "orphan child retained the reconcile lock")
                    self.assertTrue(entered.exists(), "next reconcile silently skipped after its predecessor exited")
                finally:
                    release.touch()
                    if worker.poll() is None:
                        worker.wait(timeout=5)
                    if child is not None:
                        try:
                            os.kill(child, signal.SIGTERM)
                        except ProcessLookupError:
                            pass

    def test_runtime_gate_defers_until_matching_process_drains_and_scopes_disappear(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            plan = [(root / "hostd.env", b"new-image", 0o600)]
            calls = []
            identity = "123:456"
            scopes = ["pf-vm-existing.scope"]

            def run(*args, **kwargs):
                nonlocal identity
                calls.append(args)
                if args == ("systemctl", "stop", "hostd.service"):
                    identity = None
                    return SimpleNamespace(returncode=0)
                self.fail("unexpected command: " + repr(args))

            with patch.object(host, "MAINTENANCE", root), patch.object(host, "secure_directory"), \
                    patch.object(host, "secret_file"), patch.object(host, "command", side_effect=run), \
                    patch.object(host, "service_identity", side_effect=lambda: identity), \
                    patch.object(host, "running_vm_scopes", side_effect=lambda: scopes):
                with self.assertRaises(host.InstallDeferred):
                    host.gate_runtime_install(plan, True)
                request = json.loads((root / "request.json").read_text())
                ack = dict(request, process_identity="123:old", drained=True, vms=0, assignments=0)
                (root / "state.json").write_text(json.dumps(ack))
                with self.assertRaises(host.InstallDeferred):
                    host.gate_runtime_install(plan, True)
                ack["process_identity"] = identity
                for changed in (dict(ack, token="0" * 64), dict(ack, drained=False),
                                dict(ack, assignments=1), ack):
                    (root / "state.json").write_text(json.dumps(changed))
                    with self.assertRaises(host.InstallDeferred):
                        host.gate_runtime_install(plan, True)
                self.assertEqual(calls, [], "deferred installer stopped a live daemon")
                self.assertFalse(plan[0][0].exists(), "pending runtime was installed before draining")
                scopes.clear()
                self.assertTrue(host.gate_runtime_install(plan, True))
                self.assertEqual(calls, [("systemctl", "stop", "hostd.service")])
                self.assertTrue((root / "request.json").exists(), "admission reopened before installed inputs complete")

    def test_reconcile_deferred_install_keeps_receipts_and_retries(self):
        source = (HERE / "reconcile.sh").read_text()
        phase = source[source.index("if python3 src/postflight/host/host.py install"):]
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for name in ("applied-commit", "applied-inputs"):
                (root / name).write_text("old\n")
            (root / "image-id").write_text("noble-turbo-fixture\n")
            phase = phase.replace("/opt/postflight/applied-", str(root / "applied-"))
            setup = 'set -euo pipefail\npython3() { return "${INSTALL_EXIT}"; }\n'
            env = dict(os.environ, manifest="fixture", artifacts=str(root), CRIU_VERSION="4.2",
                       commit="new-commit", inputs="new-inputs", host_id="fixture", INSTALL_EXIT="75")
            result = subprocess.run(["bash", "-c", setup + phase], env=env, capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / "applied-commit").read_text(), "old\n")
            self.assertEqual((root / "applied-inputs").read_text(), "old\n")
            self.assertIn("next timer retries", result.stderr)
            env["INSTALL_EXIT"] = "19"
            result = subprocess.run(["bash", "-c", setup + phase], env=env, capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 19, "non-defer install failure was hidden")
            env["INSTALL_EXIT"] = "0"
            result = subprocess.run(["bash", "-c", setup + phase], env=env, capture_output=True, text=True, timeout=5)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / "applied-commit").read_text(), "new-commit\n")
            self.assertEqual((root / "applied-inputs").read_text(), "new-inputs\n")

    def test_stopped_host_bootstrap_and_interrupted_install_retry(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            plan = [(root / "hostd.env", b"new-image", 0o600)]
            with patch.object(host, "MAINTENANCE", root), patch.object(host, "secure_directory"), \
                    patch.object(host, "service_identity", return_value=None), \
                    patch.object(host, "running_vm_scopes", return_value=[]), \
                    patch.object(host, "command") as command:
                self.assertTrue(host.gate_runtime_install(plan, True))
                # Installed bytes can match after a crash, but the pending
                # request still requires activation before admission reopens.
                self.assertTrue(host.gate_runtime_install(plan, False))
                command.assert_not_called()
                (root / "request.json").unlink()
                self.assertFalse(host.gate_runtime_install(plan, False))
            with patch.object(host, "MAINTENANCE", root), patch.object(host, "secure_directory"), \
                    patch.object(host, "service_identity", return_value=None), \
                    patch.object(host, "running_vm_scopes", return_value=["pf-vm-orphan.scope"]):
                with self.assertRaises(host.InstallDeferred):
                    host.gate_runtime_install(plan, True)


if __name__ == "__main__":
    unittest.main()
