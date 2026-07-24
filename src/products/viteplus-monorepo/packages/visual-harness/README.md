# @guardian/visual-harness

Deterministic Playwright visual capture for Guardian web surfaces, with two
entrypoints over one core library:

- **Local capture CLI** (`bin/capture-cli.ts`) — point it at any URL and get
  labeled, reproducible, high-resolution PNGs at fixed animation timestamps.
  Built for agent-driven visual iteration: the same flags always produce
  byte-identical output.
- **Canary** (`journeys/visual.spec.ts`) — a `@playwright/test` suite that
  fails on visual drift against git-committed baselines and on critical
  elements dropping below the fold, across the shared form-factor matrix.
  Ships as an OCI image on the pinned Playwright base.

## Determinism model

A frame is only reproducible when two independent clocks are pinned
(`src/determinism.ts`):

1. **JS time** — `page.clock` is installed at a fixed epoch and paused
   _before_ navigation, then jumped once with `fastForward(seekMs)` after a
   real-time settle. Every `requestAnimationFrame` tick and timer the page
   ever observes happens at identical mocked timestamps on every run. A
   seeded `Math.random` override makes load-time randomness (e.g. particle
   layouts) reproducible.
2. **CSS/compositor time** — `page.clock` does not touch the CSS animation
   timeline, so after settle every `Animation` (keyframes, `@property`
   gradients, pseudo-element animations) is paused and seeked to the same
   `currentTime` via the Web Animations API.

No cooperation from the page under capture is required.

## Local capture

```sh
cd src/products/viteplus-monorepo
pnpm --filter @guardian/visual-harness exec playwright install chromium  # once
pnpm --filter @guardian/visual-harness run capture -- \
  --url http://127.0.0.1:4253 --out /tmp/shots \
  --form-factor 4k-desktop,mobile --seek 0,1500,3600
```

`--help` documents all flags. Every run writes PNGs plus a `manifest.json`
recording viewport, scale factor, seed, seek, and sha256 per shot. The
`4k-desktop` profile is 1920×1080 at `deviceScaleFactor` 2 — a physical
3840×2160 capture.

## Canary contract

Configured by env, mirroring the journey canaries:

| Variable              | Default                      | Meaning                                                           |
| --------------------- | ---------------------------- | ----------------------------------------------------------------- |
| `VISUAL_TARGET_URL`   | (required)                   | Absolute URL; HTTPS unless loopback or `VISUAL_ALLOW_HTTP=1`      |
| `VISUAL_TARGET`       | `shortty`                    | Target profile in `src/targets/` (critical selectors, tolerances) |
| `VISUAL_FORM_FACTORS` | `all`                        | Comma list from `src/form-factors.ts`                             |
| `VISUAL_SEEK_MS`      | `3600`                       | Frozen animation timestamp                                        |
| `VISUAL_TIMEOUT`      | `2m`                         | Per-test budget, Go-duration, 1m–5m                               |
| `VISUAL_OUTPUT_DIR`   | `/tmp/visual-harness-output` | Diff artifacts (the pod's only writable mount)                    |

Output is one JSON line per event (`begin`, `test`, `finding`, `end`);
findings are `fold-drop` or `visual-drift`. Exit code 0 = clean, non-zero =
findings or harness error.

## Baselines

Snapshots live in `journeys/visual.spec.ts-snapshots/` and are committed to
git — a drift is reviewed as a PR image diff. The snapshot name embeds the
platform, and **linux baselines are the only ones the canary and CI gate
compare against**: font antialiasing differs per OS, so baselines must be
generated inside the pinned Playwright image, never on a laptop.
macOS-suffixed snapshot files must not be committed.

Run the canary with `--update-snapshots=missing` and a form factor without a
committed baseline gets one generated against the exact built shortty image,
left in the output directory to review and commit. Once committed, any
mismatch fails the run with the diff alongside it. To capture or refresh
baselines after an intentional visual change, on a linux box:

```sh
bazelisk run //src/products/viteplus-monorepo/packages/visual-harness:load
docker run --rm \
  -e VISUAL_TARGET_URL=... -e VISUAL_ALLOW_HTTP=1 \
  -v "$PWD/src/products/viteplus-monorepo/packages/visual-harness/journeys:/app/journeys" \
  guardian/visual-harness:dev --update-snapshots
```

(or delete the stale baseline files and let the gate regenerate them for
review).

## Adding a target

Add `src/targets/<name>.ts` exporting a `TargetConfig` (critical selectors =
what must be visible without scrolling; a `waitSelector` that proves
hydration), register it in `TARGETS` in `src/config.ts`, generate baselines
in-image, and commit them.
