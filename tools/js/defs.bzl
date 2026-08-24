"""vitest under Bazel.

One test target per workspace package, running the package's own
`vp test run` inside a scratch copy of the pnpm workspace — the same
layout `//src/postflight/checkout:generated_bundle` builds in, so a suite
that passes here passes for `vp test run` at the repo root and vice
versa. Fixtures a suite reads from the repo (committed wasm artifacts,
goldens) ride in through `data`.
"""

load("@rules_shell//shell:sh_test.bzl", "sh_test")

def vitest_test(name, package_dir, data = [], **kwargs):
    sh_test(
        name = name,
        srcs = ["//tools/js:vitest_test.sh"],
        args = [package_dir, "$(rootpaths //:workspace_sources)"] + ["$(rootpaths %s)" % d for d in data],
        data = [
            ":node_modules",
            ":node_modules/vite-plus",
            "//:node_modules/vite-plus",
            "//:vp_node",
            "//:workspace_sources",
        ] + data,
        **kwargs
    )
