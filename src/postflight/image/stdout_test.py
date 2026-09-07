"""Exercise production builder phases with noisy, unprivileged command doubles."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


HERE = Path(__file__).resolve().parent


def section(source, start, end=None):
    """Keep production control flow/redirections, excluding privileged setup."""
    begin = source.index(start)
    return source[begin:source.index(end, begin)] if end else source[begin:]


class StdoutTest(unittest.TestCase):
    def run_phase(self, script, root, **env):
        return subprocess.run(
            ["bash", "-c", "set -euo pipefail\n" + script],
            cwd=root, env={**os.environ, **env}, text=True, capture_output=True,
            timeout=15,
        )

    def test_runtime_commands_leave_only_image_id_on_stdout(self):
        source = (HERE / "build.sh").read_text()
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            guest = root / "guest"
            for directory in (
                "etc/modules-load.d", "etc/sudoers.d", "etc/systemd/system",
                "etc/systemd/network", "etc/postflight", "usr/local/libexec",
            ):
                (guest / directory).mkdir(parents=True)
            scratch = root / "scratch-device"
            scratch.touch()
            # The real helper and runtime/templating phases execute below.
            # Host mutation tools are inert; commands that normally report
            # progress deliberately write to stdout. Data queries stay real
            # contract-shaped, including CRIU version and ZFS send/recv bytes.
            mocks = r'''
log() { echo "$*" >&2; }
die() { echo "$*" >&2; exit 1; }
chroot() {
  case "$*" in
    *"/usr/sbin/criu --version") echo 'Version: fixture' ;;
    *) echo "chroot output: $*" ;;
  esac
}
find() { echo fixture-kernel; }
nproc() { echo 4; }
install() { :; }
chmod() { :; }
tar() { :; }
rm() { :; }
mv() { :; }
ln() { :; }
touch() { :; }
python3() {
  if [[ "$1" == fixture/image_identity.py ]]; then return 0; fi
  command python3 "$@"
}
qemu-img() {
  if [[ "$1" == info ]]; then echo '{"virtual-size":1048576}';
  else echo "qemu-img output: $*"; fi
}
zfs() {
  case "$1" in
    send) printf 'snapshot-stream' ;;
    recv) [[ "$(cat)" == snapshot-stream ]]; echo 'zfs receive output' ;;
    *) echo "zfs output: $*" ;;
  esac
}
'''
            script = mocks + section(source, "in_chroot() {", '\nmkdir -p "${work_dir}"')
            script += section(source, 'log "installing Postflight runtime dependencies"', 'rootfs_free_bytes=')
            template = section(source, 'log "templating ${dataset}@golden"')
            # A regular fixture file stands in for the already-created zvol;
            # every surrounding production check and pipeline is unchanged.
            template = template.replace('scratch_device="/dev/zvol/${scratch}"',
                                        'scratch_device="${test_scratch_device}"')
            script += template
            result = self.run_phase(
                script, root, mnt=str(guest), work_dir=str(root),
                CRIU_VERSION="fixture", CRIU_COMMIT="fixture", TINI_VERSION="fixture",
                RUNNER_VERSION="fixture", runner_tarball="runner.tar.gz", criu_tarball="criu.tar.gz",
                RUNNER_LISTENER_DLL="listener", GUESTD_BIN="guestd", guestd_sha256="fixture",
                pool="test", dataset="test/images/noble-turbo-fixture", scratch="test/build/fixture",
                work_image="fixture.qcow2", image_id="noble-turbo-fixture",
                script_dir="fixture", identity_file="fixture-identity", receipt="fixture-receipt",
                test_scratch_device=str(scratch),
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, "noble-turbo-fixture\n")
            for command in ("apt-get", "make", "installdependencies.sh", "systemctl", "qemu-img output", "zfs receive output"):
                self.assertIn(command, result.stderr)

    def test_packer_cold_build_result_and_failure(self):
        source = (HERE / "build-upstream.sh").read_text()
        phase = section(source, 'log "initializing Packer QEMU plugin')
        for failure in (False, True):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                plugin = root / "plugins/github.com/hashicorp/qemu/packer-plugin-qemu_vfixture_x5.0_linux_amd64"
                plugin.parent.mkdir(parents=True)
                plugin.touch(mode=0o755)
                packer = root / "packer"
                packer.write_text('''#!/usr/bin/env bash
set -eu
echo "packer $1 stdout"
echo "packer $1 stderr" >&2
if [[ "$1" == build ]]; then
  [[ "$TEST_PACKER_FAIL" != yes ]] || exit 19
  mkdir -p "$building_dir"
  : >"$building_dir/runner-images.qcow2"
fi
''')
                packer.chmod(0o755)
                mocks = r'''
log() { echo "$*" >&2; }
die() { echo "$*" >&2; exit 1; }
sha256sum() { echo 'fixture  plugin'; }
timeout() { shift 2; "$@"; }
qemu-img() { echo 'qemu integrity output'; }
'''
                cached = root / "cache/runner-images.qcow2"
                result = self.run_phase(
                    mocks + phase, root, packer=str(packer), PACKER_QEMU_PLUGIN_VERSION="fixture",
                    PACKER_PLUGIN_PATH=str(root / "plugins"), PACKER_QEMU_PLUGIN_SHA256="fixture",
                    rendered_template="fixture.hcl", cloud_init_dir=str(root),
                    RUNNER_IMAGES_VERSION="fixture", RUNNER_IMAGES_REF="fixture", UBUNTU_SHA256="fixture",
                    building_dir=str(root / "building"), log_file=str(root / "build.log"),
                    cache_dir=str(root / "cache"), cached_image=str(cached), packer_timeout="8h",
                    qemu_accelerator="tcg", qemu_binary="qemu", qemu_cpus="4", qemu_cpu_model="max",
                    qemu_memory_mib="1024", ubuntu_url="unused", private_key="unused",
                    TEST_PACKER_FAIL="yes" if failure else "no",
                )
                self.assertEqual(result.returncode, 1 if failure else 0, result.stderr)
                self.assertEqual(result.stdout, "" if failure else str(cached) + "\n")
                self.assertEqual(cached.exists(), not failure)
                for operation in ("init", "validate", "build"):
                    self.assertIn(f"packer {operation} stdout", result.stderr)
                log = (root / "build.log").read_text()
                self.assertIn("packer build stdout", log)
                self.assertIn("packer build stderr", log)

    def test_both_cache_hits_return_only_the_result(self):
        upstream = (HERE / "build-upstream.sh").read_text()
        runtime = (HERE / "build.sh").read_text()
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            cached = root / "cached.qcow2"
            cached.touch()
            for source, start, end, expected in (
                (upstream, 'if [[ -f "${cached_image}" ]]', '[[ ! -e "${cache_dir}" ]]', str(cached)),
                (runtime, 'if zfs list -H -o name "${dataset}@golden"', 'fetch "${runner_url}"', "noble-turbo-fixture"),
            ):
                result = self.run_phase(
                    'log() { echo "$*" >&2; }\nqemu-img() { echo integrity; }\nzfs() { echo snapshot; }\npython3() { :; }\n' +
                    section(source, start, end), root,
                    cached_image=str(cached), dataset="test/images/fixture", image_id="noble-turbo-fixture",
                    script_dir="fixture", identity_file="fixture-identity", receipt="fixture-receipt",
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, expected + "\n")


if __name__ == "__main__":
    unittest.main()
