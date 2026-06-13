"""Formatter target conventions."""

GO_FORMAT_TAGS = [
    "guardian-format",
    "guardian-format-gofmt",
]

def _go_format_impl(ctx):
    sdk = ctx.toolchains["@io_bazel_rules_go//go:toolchain"].sdk
    gofmt = None
    for tool in sdk.tools.to_list():
        if tool.basename == "gofmt" or tool.basename == "gofmt.exe":
            gofmt = tool
            break
    if gofmt == None:
        fail("rules_go SDK did not expose gofmt")
    executable = ctx.actions.declare_file(ctx.label.name)
    ctx.actions.symlink(
        output = executable,
        target_file = gofmt,
        is_executable = True,
    )
    return [DefaultInfo(
        executable = executable,
        files = depset([executable]),
        runfiles = ctx.runfiles(files = [gofmt]),
    )]

_go_format = rule(
    implementation = _go_format_impl,
    executable = True,
    toolchains = ["@io_bazel_rules_go//go:toolchain"],
)

def go_format(name = "format", tags = [], **kwargs):
    _go_format(
        name = name,
        tags = tags + GO_FORMAT_TAGS,
        **kwargs
    )
