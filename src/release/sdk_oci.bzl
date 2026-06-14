"""Rules for SDK OCI artifact outputs."""

_ZERO_COMMIT = "0000000000000000000000000000000000000000"

def _sdk_oci_layout_impl(ctx):
    layout = ctx.actions.declare_directory(ctx.label.name + ".oci")
    result = ctx.actions.declare_file(ctx.label.name + ".json")

    args = ctx.actions.args()
    args.add(ctx.info_file)
    args.add(ctx.executable.sdkoci)
    args.add(ctx.file.tarball)
    args.add(ctx.file.pack_json)
    args.add(layout.path)
    args.add(ctx.attr.tag)
    args.add(result.path)
    args.add(ctx.attr.source_repo)
    args.add(ctx.attr.source_commit)
    args.add(ctx.attr.default_source_commit)
    args.add(ctx.attr.artifact_type)
    args.add(ctx.attr.payload_media_type)
    args.add(ctx.attr.distributable)
    args.add(ctx.attr.payload_form)
    args.add(ctx.attr.description)
    args.add(ctx.attr.expected_package)
    args.add(ctx.attr.filename_suffix)

    ctx.actions.run_shell(
        command = """
set -euo pipefail
status="$1"
sdkoci="$2"
tarball="$3"
pack_json="$4"
layout="$5"
tag="$6"
result="$7"
source_repo="$8"
source_commit="$9"
default_source_commit="${10}"
artifact_type="${11}"
payload_media_type="${12}"
distributable="${13}"
payload_form="${14}"
description="${15}"
expected_package="${16}"
filename_suffix="${17}"
if [ -z "$source_commit" ]; then
  source_commit="$(awk '$1 == "BUILD_EMBED_LABEL" && $2 != "" { print $2; exit }' "$status")"
fi
if [ -z "$source_commit" ]; then
  source_commit="$default_source_commit"
fi
"$sdkoci" \
  --tarball "$tarball" \
  --pack-json "$pack_json" \
  --oci-layout "$layout" \
  --tag "$tag" \
  --output "$result" \
  --source-repo "$source_repo" \
  --source-commit "$source_commit" \
  --artifact-type "$artifact_type" \
  --payload-media-type "$payload_media_type" \
  --distributable "$distributable" \
  --payload-form "$payload_form" \
  --description "$description" \
  --expected-package "$expected_package" \
  --filename-suffix "$filename_suffix" >/dev/null
""",
        arguments = [args],
        inputs = [
            ctx.file.tarball,
            ctx.file.pack_json,
            ctx.info_file,
        ],
        outputs = [
            layout,
            result,
        ],
        tools = [ctx.executable.sdkoci],
        mnemonic = "GuardianSDKOCI",
        progress_message = "Building SDK OCI artifact %{label}",
    )

    return DefaultInfo(files = depset([layout, result]))

guardian_sdk_oci_layout = rule(
    implementation = _sdk_oci_layout_impl,
    attrs = {
        "artifact_type": attr.string(default = "application/vnd.guardian.sdk.npm.package.v1"),
        "default_source_commit": attr.string(default = _ZERO_COMMIT),
        "description": attr.string(default = "Guardian aisucks TypeScript SDK npm package tarball"),
        "distributable": attr.string(default = "aisucks-ts-sdk"),
        "expected_package": attr.string(default = "@guardian-intelligence/aisucks"),
        "filename_suffix": attr.string(default = ".tgz"),
        "pack_json": attr.label(allow_single_file = True, mandatory = True),
        "payload_form": attr.string(default = "npm"),
        "payload_media_type": attr.string(default = "application/gzip"),
        "sdkoci": attr.label(
            cfg = "exec",
            default = "//src/release/cmd/sdkoci",
            executable = True,
        ),
        "source_commit": attr.string(),
        "source_repo": attr.string(default = "https://github.com/guardian-intelligence/guardian"),
        "tag": attr.string(default = "edge"),
        "tarball": attr.label(allow_single_file = True, mandatory = True),
    },
)
