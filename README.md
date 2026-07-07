# guardian

Guardian runs on a Cozystack-native management cluster. Development is done via GitOps.

## Quickstart

Run `eval "$(scripts/bootstrap.sh path)" && aspect tools install && eval "$(aspect tools path)"`
to install the pinned bootstrap toolchain plus repo-pinned CLIs and build tools,
`aspect build`, `aspect lint`, `aspect test`, and `aspect tidy` to build, lint,
test, and format the repo (fast with cache), and `aspect --help` /
`aspect <task> --help` to view development tasks and their options.

Deeper reading: `AGENTS.md` (conventions and the durable command surface), the
web frontend dev loop in `src/products/viteplus-monorepo/README.md`, the
runbooks in `src/infrastructure/runbooks/`, and the design docs in `docs/`.

Generated Talm secrets, rendered node configs, kubeconfigs, and local operator
state stay out of Git.
