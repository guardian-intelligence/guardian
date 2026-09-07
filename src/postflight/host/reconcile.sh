#!/usr/bin/env bash
# Follow the protected public main branch. All executable inputs and build
# caches used as root are isolated from the interactive ubuntu checkout.
set -euo pipefail
umask 027
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin
export GIT_TERMINAL_PROMPT=0
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES

[[ "${EUID}" -eq 0 && "$(uname -s)" == Linux ]] || { echo "Linux root is required" >&2; exit 1; }
host_id="${1:?usage: reconcile.sh HOST_ID}"
[[ "${host_id}" =~ ^[a-z][a-z0-9-]{0,30}$ ]] || { echo "invalid host ID" >&2; exit 1; }
[[ "$#" -le 2 && ( -z "${2:-}" || "${2:-}" == --lock-held ) ]] || { echo "unexpected reconcile argument" >&2; exit 1; }
origin=https://github.com/guardian-intelligence/guardian.git
source_dir=/opt/postflight/source
artifact_root=/opt/postflight/artifacts

# Verify before using mkdir, lock redirects, git, or a build cache. In
# particular, never follow a symlink left by another account under /var/tmp.
python3 - <<'PY'
import os
from pathlib import Path
import stat
for name in ("/opt/postflight", "/opt/postflight/source", "/opt/postflight/artifacts",
             "/opt/postflight/bootstrap-bin", "/opt/postflight/bazel",
             "/run/postflight-reconcile", "/var/tmp/postflight-ci-image", "/var/tmp/postflight-ci-runner"):
    path = Path(name)
    if path.exists() or path.is_symlink():
        info = path.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise SystemExit("untrusted root build directory: " + name)
    else:
        path.mkdir(mode=0o700, parents=True)
PY
# Only flock's supervisor owns the lock descriptor. --close removes it from
# the re-entered shell and every build subprocess, including servers that
# outlive a failed reconcile. The private argument is not inherited by later
# invocations, unlike an environment flag. Contention remains a successful skip.
lock_file=/run/postflight-reconcile/reconcile.lock
if [[ "${2:-}" != --lock-held ]]; then
  exec flock --nonblock --conflict-exit-code 0 --close "${lock_file}" "${BASH}" "$0" "${host_id}" --lock-held
fi

if [[ ! -d "${source_dir}/.git" ]]; then
  [[ -z "$(ls -A "${source_dir}")" ]] || { echo "nonempty source directory is not our Git repository" >&2; exit 1; }
  git clone --no-checkout --single-branch --branch main "${origin}" "${source_dir}"
fi
[[ "$(git -C "${source_dir}" remote get-url origin)" == "${origin}" ]] || { echo "unexpected source origin" >&2; exit 1; }
git -C "${source_dir}" -c core.hooksPath=/dev/null fetch --prune origin refs/heads/main:refs/remotes/origin/main
if [[ -f /opt/postflight/applied-commit ]]; then
  git -C "${source_dir}" merge-base --is-ancestor "$(cat /opt/postflight/applied-commit)" origin/main || {
    echo "protected main rewound; refusing to replace the applied source" >&2; exit 1;
  }
fi
git -C "${source_dir}" -c core.hooksPath=/dev/null checkout --detach --force origin/main
cd "${source_dir}"
commit="$(git rev-parse HEAD)"
manifest="${source_dir}/src/postflight/host/hosts/${host_id}.json"
[[ -f "${manifest}" ]] || { echo "merged host manifest does not exist: ${host_id}" >&2; exit 1; }

# A commit elsewhere in the monorepo does not build or rotate runner images.
# Include every internal Go dependency plus build/pin inputs, not HEAD itself.
inputs="$(git ls-tree -r HEAD -- \
  src/postflight/host src/postflight/hostd src/postflight/guestd \
  src/postflight/generation src/postflight/timing src/postflight/image src/postflight/runner \
  go.mod go.sum MODULE.bazel MODULE.bazel.lock BUILD.bazel \
  .bazelversion .bazelrc .bazeliskrc scripts/bootstrap.sh scripts/bootstrap tools .aspect \
  | sha256sum | cut -d' ' -f1)"
artifacts="${artifact_root}/${inputs}"
install -d -m 0700 "${artifacts}"
python3 src/postflight/host/host.py storage --manifest "${manifest}"

readarray -t host_values < <(python3 - "${manifest}" <<'PY'
import json
import sys
m = json.load(open(sys.argv[1]))
for key in ("pool", "qemu_path", "qemu_version"):
    print(m[key])
PY
)
[[ "$("${host_values[1]}" --version | head -n 1)" == "${host_values[2]}" ]] || {
  echo "QEMU differs from the reviewed host version pin" >&2; exit 1;
}

if [[ ! -s "${artifacts}/image-id" || ! -x "${artifacts}/hostd" ]]; then
  # Bootstrap downloads only the version/hash-pinned Bazelisk/Aspect pair.
  eval "$(BOOTSTRAP_INSTALL_DIR=/opt/postflight/bootstrap-bin scripts/bootstrap.sh path)"
  bazelisk --output_user_root=/opt/postflight/bazel build //src/postflight/hostd/cmd/hostd:hostd //src/postflight/guestd/cmd/guestd:guestd
  hostd="$(bazelisk --output_user_root=/opt/postflight/bazel cquery --output=files //src/postflight/hostd/cmd/hostd:hostd)"
  guestd="$(bazelisk --output_user_root=/opt/postflight/bazel cquery --output=files //src/postflight/guestd/cmd/guestd:guestd)"
  install -m 0755 "${hostd}" "${artifacts}/hostd"
  install -m 0755 "${guestd}" "${artifacts}/guestd"
  listener="$(WORK_DIR=/var/tmp/postflight-ci-runner src/postflight/runner/build.sh)"
  install -m 0644 "${listener}" "${artifacts}/Runner.Listener.dll"
  # Host and image identities are separate: host-only changes rebuild/check
  # these artifacts but reuse a golden image whose actual guest inputs match.
  image_id="$(POOL="${host_values[0]}" IMAGE_FLAVOR=turbo WORK_DIR=/var/tmp/postflight-ci-image \
    QEMU_BINARY="${host_values[1]}" GUESTD_BIN="${artifacts}/guestd" \
    RUNNER_LISTENER_DLL="${artifacts}/Runner.Listener.dll" src/postflight/image/build.sh)"
  [[ "${image_id}" =~ ^noble-turbo-[a-z0-9-]+$ ]] || { echo "invalid golden image result" >&2; exit 1; }
  printf '%s\n' "${image_id}" >"${artifacts}/image-id"
else
  echo "Postflight source inputs unchanged; reusing ${inputs}" >&2
fi

# shellcheck source=../image/pins.env
source src/postflight/image/pins.env
if python3 src/postflight/host/host.py install --manifest "${manifest}" \
  --hostd "${artifacts}/hostd" --image-id "$(cat "${artifacts}/image-id")" \
  --criu-version "${CRIU_VERSION}" --enable-reconcile; then
  :
else
  install_status=$?
  if [[ "${install_status}" -eq 75 ]]; then
    echo "Postflight install pending drain; next timer retries without marking inputs applied" >&2
    exit 0
  fi
  exit "${install_status}"
fi
printf '%s\n' "${commit}" >/opt/postflight/applied-commit.tmp
mv /opt/postflight/applied-commit.tmp /opt/postflight/applied-commit
printf '%s\n' "${inputs}" >/opt/postflight/applied-inputs.tmp
mv /opt/postflight/applied-inputs.tmp /opt/postflight/applied-inputs
echo "Postflight ${host_id} converged source=${commit} inputs=${inputs}"
