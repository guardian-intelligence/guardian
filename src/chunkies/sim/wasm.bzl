# Builds a Rust cdylib as a wasm32 module with a pinned compilation mode,
# so the emitted bytes are identical no matter what -c mode the invoking
# build uses. That stability is what lets the committed module artifacts be
# guarded by exact byte-diff tests.

def _wasm_transition_impl(settings, attr):
    return {
        "//command_line_option:platforms": [Label("//src/chunkies/sim:wasm32")],
        "//command_line_option:compilation_mode": "opt",
    }

_wasm_transition = transition(
    implementation = _wasm_transition_impl,
    inputs = [],
    outputs = [
        "//command_line_option:platforms",
        "//command_line_option:compilation_mode",
    ],
)

def _wasm_module_impl(ctx):
    files = ctx.attr.lib[0][DefaultInfo].files.to_list()
    src = [f for f in files if f.extension == "wasm"]
    if len(src) != 1:
        fail("expected exactly one .wasm output, got: %s" % files)
    out = ctx.actions.declare_file(ctx.attr.out)
    ctx.actions.symlink(output = out, target_file = src[0])
    return [DefaultInfo(files = depset([out]))]

wasm_module = rule(
    implementation = _wasm_module_impl,
    attrs = {
        "lib": attr.label(cfg = _wasm_transition, mandatory = True),
        "out": attr.string(mandatory = True),
        "_allowlist_function_transition": attr.label(
            default = "@bazel_tools//tools/allowlists/function_transition_allowlist",
        ),
    },
)
