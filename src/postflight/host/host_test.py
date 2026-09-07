import copy
import ipaddress
import json
from pathlib import Path
import stat
import subprocess
import tempfile
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
                           ("slots", True), ("cpus", 8)):
            with self.subTest(key=key, value=value), tempfile.TemporaryDirectory() as temp:
                changed = copy.deepcopy(self.manifest)
                changed[key] = value
                path = Path(temp) / "host.json"
                path.write_text(json.dumps(changed))
                with self.assertRaises(ValueError):
                    host.load_manifest(path)
        with self.assertRaises(ValueError):
            host.render(self.manifest, "noble-confidential-test", "4.2")

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


if __name__ == "__main__":
    unittest.main()
