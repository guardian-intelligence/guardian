# @guardian/visual-harness

Deterministic Playwright visual capture for Guardian web surfaces: a local
capture CLI (`bin/capture-cli.ts`) for reproducible labeled PNGs at fixed
animation timestamps, and a canary (`journeys/visual.spec.ts`) that fails on
visual drift against git-committed baselines and on critical elements dropping
below the fold. The determinism model (pinned JS clock + seeded random before
navigation, one `fastForward`, then a WAAPI freeze) is documented in
`src/determinism.ts`.

## Local capture

```sh
cd src/products/viteplus-monorepo
pnpm --filter @guardian/visual-harness exec playwright install chromium  # once
pnpm --filter @guardian/visual-harness run capture -- \
  --url http://127.0.0.1:4253 --out /tmp/shots \
  --form-factor 4k-desktop,mobile --seek 0,1500,3600
```

`--help` documents all flags; `VISUAL_HTML_IN_CANVAS=1` enables Chromium's
`CanvasDrawElement` feature to exercise the native HTML-in-canvas path.

## Canary

Configured by env: `VISUAL_TARGET_URL` (required), `VISUAL_TARGET`
(default `privatecut`, profiles in `src/targets/`), `VISUAL_FORM_FACTORS`,
`VISUAL_SEEK_MS`, `VISUAL_TIMEOUT`, `VISUAL_OUTPUT_DIR`. Emits one JSON line
per event; exit code 0 = clean.

## Baselines

Committed under `journeys/visual.spec.ts-snapshots/`; linux baselines are the
only ones CI compares against, so generate them inside the pinned Playwright
image, never on a laptop:

```sh
bazelisk run //src/products/viteplus-monorepo/packages/visual-harness:load
docker run --rm \
  -e VISUAL_TARGET_URL=... -e VISUAL_ALLOW_HTTP=1 \
  -v "$PWD/src/products/viteplus-monorepo/packages/visual-harness/journeys:/app/journeys" \
  guardian/visual-harness:dev --update-snapshots
```

New targets: add `src/targets/<name>.ts`, register it in `src/config.ts`,
generate baselines in-image, and commit them.
