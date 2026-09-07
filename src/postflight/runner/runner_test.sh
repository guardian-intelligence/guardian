#!/usr/bin/env bash
set -euo pipefail

build_sh="${1:?}"
patch_file="${2:?}"
pins="${3:?}"
runner_source="${4:?}"
module="${5:?}"
bash -n "${build_sh}"
grep -Fq 'PublishPostflightAssignmentAsync(jobMessage.RequestId' "${patch_file}"
grep -Fq 'PublishPostflightAssignmentAsync(messageRef.RunnerRequestId' "${patch_file}"
grep -Fq 'jobDispatcher.Run(jobMessage' "${patch_file}"
grep -Fq 'jobDispatcher.Run(jobRequestMessage' "${patch_file}"
grep -Fq 'SystemVariable("system.github.job")' "${patch_file}"
grep -Fq 'JobContextNumber("check_run_id")' "${patch_file}"
grep -Fq 'RUNNER_SOURCE_COMMIT=' "${pins}"
grep -Fq 'DOTNET_SDK_SHA512=' "${pins}"
grep -Fq 'runner-listener.patch" >&2' "${build_sh}"
grep -Fq 'patch --fuzz=0' "${build_sh}"
grep -Fq -- '-o "${output_root}" >&2' "${build_sh}"

# Exercise the real upstream source: the previous zero-context patch passed
# string checks while inserting the helper into RunAsync and failing C# build.
source "${pins}"
grep -Fq "actions/runner/${RUNNER_SOURCE_COMMIT}/src/Runner.Listener/Runner.cs" "${module}"
scratch="$(mktemp -d "${TEST_TMPDIR:-/tmp}/postflight-runner-patch.XXXXXX")"
trap 'rm -rf "${scratch}"' EXIT
mkdir -p "${scratch}/src/Runner.Listener"
cp "${runner_source}" "${scratch}/src/Runner.Listener/Runner.cs"
patch --batch --fuzz=0 --directory="${scratch}" --strip=1 <"${patch_file}"
bash "${build_sh}" --help >/dev/null
if bash "${build_sh}" --unknown >/dev/null 2>&1; then
  echo "runner build accepted an unknown flag" >&2
  exit 1
fi
