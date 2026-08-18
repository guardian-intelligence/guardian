# Web workspace

The Guardian web frontend — TanStack Start (SSR on nitro), bundled with
vite-plus. These apps and packages are members of the repo-rooted pnpm
workspace; `pnpm-workspace.yaml`, `package.json`, the catalog, and
`pnpm-lock.yaml` live at the repo root, and every command below runs from
there.

## Dev loop

```bash
vp install
vp dev
vp run ready   # pre-merge gate: lint + test + typecheck + build
```

## Build / ship

The shippable image builds through Bazel — `vp build` runs as a reproducible
genrule inside the OCI image target, so `bazelisk build //...` covers it and no
separate build system ships:

```bash
bazelisk build //src/company/web/site:image
```

CI's `images` workflow builds and publishes that target. `pnpm run build` runs the
same vite build for local inspection.
