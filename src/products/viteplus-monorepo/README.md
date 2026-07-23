# viteplus-monorepo

The Guardian web frontend — TanStack Start (SSR on nitro), bundled with
vite-plus. `node_modules` is pinned by `pnpm-lock.yaml`.

## Dev loop

```bash
pnpm install
pnpm run dev      # guardianintelligence-web dev server (HMR)
pnpm run ready    # pre-merge gate: lint + test + typecheck + build
```

## Build / ship

The shippable image builds through Bazel — `vp build` runs as a reproducible
genrule inside the OCI image target, so `bazelisk build //...` covers it and no
separate build system ships:

```bash
bazelisk build //src/products/viteplus-monorepo/apps/guardianintelligence-web/site:image
```

CI's `images` workflow builds and publishes that target. `pnpm run build` runs the
same vite build for local inspection.
