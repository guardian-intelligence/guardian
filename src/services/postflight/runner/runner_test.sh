#!/usr/bin/env bash
set -euo pipefail

build_sh="${1:?}"
patch_file="${2:?}"
pins="${3:?}"
bash -n "${build_sh}"
grep -Fq 'WaitForPostflightAssignmentAsync(jobMessage.RequestId' "${patch_file}"
grep -Fq 'WaitForPostflightAssignmentAsync(messageRef.RunnerRequestId' "${patch_file}"
grep -Fq 'jobDispatcher.Run(jobMessage' "${patch_file}"
grep -Fq 'jobDispatcher.Run(jobRequestMessage' "${patch_file}"
grep -Fq 'SystemVariable("system.github.job")' "${patch_file}"
grep -Fq 'RUNNER_SOURCE_COMMIT=' "${pins}"
grep -Fq 'DOTNET_SDK_SHA512=' "${pins}"
grep -Fq 'runner-listener.patch" >&2' "${build_sh}"
grep -Fq -- '-o "${output_root}" >&2' "${build_sh}"
bash "${build_sh}" --help >/dev/null
if bash "${build_sh}" --unknown >/dev/null 2>&1; then
  echo "runner build accepted an unknown flag" >&2
  exit 1
fi
