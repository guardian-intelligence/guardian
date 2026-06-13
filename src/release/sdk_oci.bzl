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
  --source-commit "$source_commit" >/dev/null
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
        "default_source_commit": attr.string(default = _ZERO_COMMIT),
        "pack_json": attr.label(allow_single_file = True, mandatory = True),
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
