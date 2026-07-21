#!/usr/bin/env bash
# Rebuild only Runner.Listener from the exact actions/runner source used by
# the official tarball. The output DLL is dropped into that tarball by the
# golden-image builder; every other official runner byte remains unchanged.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pins="${script_dir}/../image/pins.env"

usage() {
  echo "usage: build.sh [--help]" >&2
  echo "prints the absolute path to the patched Runner.Listener.dll" >&2
}
if [[ "$#" -gt 0 ]]; then
  case "$1" in
  -h | --help) usage; exit 0 ;;
  *) usage; exit 1 ;;
  esac
fi

# shellcheck source=../image/pins.env
source "${pins}"
for var in RUNNER_VERSION RUNNER_SOURCE_COMMIT RUNNER_SOURCE_SHA256 DOTNET_SDK_VERSION DOTNET_SDK_SHA512; do
  [[ -n "${!var:-}" ]] || { echo "build.sh: missing ${var}" >&2; exit 1; }
done

work_dir="${WORK_DIR:-/var/tmp/postflight-runner}"
source_archive="${work_dir}/actions-runner-${RUNNER_SOURCE_COMMIT}.tar.gz"
source_url="https://github.com/actions/runner/archive/${RUNNER_SOURCE_COMMIT}.tar.gz"
sdk_archive="${work_dir}/dotnet-sdk-${DOTNET_SDK_VERSION}-linux-x64.tar.gz"
sdk_url="https://builds.dotnet.microsoft.com/dotnet/Sdk/${DOTNET_SDK_VERSION}/dotnet-sdk-${DOTNET_SDK_VERSION}-linux-x64.tar.gz"
patch_sha256="$(sha256sum "${script_dir}/runner-listener.patch" | awk '{print $1}')"
source_root="${work_dir}/actions-runner-${RUNNER_SOURCE_COMMIT}-${patch_sha256:0:12}"
sdk_root="${work_dir}/dotnet-${DOTNET_SDK_VERSION}"
output_root="${work_dir}/output-${RUNNER_SOURCE_COMMIT}-${patch_sha256:0:12}"

mkdir -p "${work_dir}"
fetch_sha256() {
  local url="$1" dest="$2" expected="$3"
  if [[ -f "${dest}" && "$(sha256sum "${dest}" | awk '{print $1}')" == "${expected}" ]]; then return; fi
  curl -fsSL --retry 3 -o "${dest}.partial" "${url}"
  [[ "$(sha256sum "${dest}.partial" | awk '{print $1}')" == "${expected}" ]] || { echo "build.sh: source checksum mismatch" >&2; exit 1; }
  mv "${dest}.partial" "${dest}"
}
fetch_sha512() {
  local url="$1" dest="$2" expected="$3"
  if [[ -f "${dest}" && "$(sha512sum "${dest}" | awk '{print $1}')" == "${expected}" ]]; then return; fi
  curl -fsSL --retry 3 -o "${dest}.partial" "${url}"
  [[ "$(sha512sum "${dest}.partial" | awk '{print $1}')" == "${expected}" ]] || { echo "build.sh: SDK checksum mismatch" >&2; exit 1; }
  mv "${dest}.partial" "${dest}"
}

fetch_sha256 "${source_url}" "${source_archive}" "${RUNNER_SOURCE_SHA256}"
fetch_sha512 "${sdk_url}" "${sdk_archive}" "${DOTNET_SDK_SHA512}"
if [[ ! -f "${source_root}/.postflight-patched" ]]; then
  mkdir -p "${source_root}"
  tar -xzf "${source_archive}" -C "${source_root}" --strip-components=1
  patch --directory="${source_root}" --strip=1 <"${script_dir}/runner-listener.patch"
  touch "${source_root}/.postflight-patched"
fi
if [[ ! -x "${sdk_root}/dotnet" ]]; then
  mkdir -p "${sdk_root}"
  tar -xzf "${sdk_archive}" -C "${sdk_root}"
fi
mkdir -p "${output_root}"
"${sdk_root}/dotnet" build "${source_root}/src/Runner.Listener/Runner.Listener.csproj" \
  --configuration Release --runtime linux-x64 \
  -p:RunnerVersion="${RUNNER_VERSION}" -p:PackageRuntime=linux-x64 \
  -o "${output_root}"

artifact="${output_root}/Runner.Listener.dll"
[[ -f "${artifact}" ]] || { echo "build.sh: Runner.Listener.dll was not produced" >&2; exit 1; }
realpath "${artifact}"
