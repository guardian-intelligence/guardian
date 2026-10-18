# PrivateCut — rumi.engineering

Browser-native video clipper: mediabunny in a worker cuts up to a minute of any video at the best quality that fits under a hard size cap, gated by measured output size — nothing uploads. TanStack Start (React, SSR on nitro) + Tailwind, with a WebGL2 canvas compositor that progressively enables native HTML-in-canvas; ships as a Bazel-built OCI image.

Dev loop: `vp install && vp dev`, then `vp run ready` as the pre-merge gate (lint + test + typecheck + build).

Visual testing: `packages/visual-harness` captures deterministic frames and gates drift against committed baselines — see its README for local capture and baseline refresh.

Encode regressions: `vp run regression:encode` (with `vp dev` running; `BASE=` to point elsewhere, `ONLY=` to pick scenarios) drives headed Chrome through failures that only reproduce on real hardware codecs — the AAC floor that crashes the GPU process, the encoder rate floor, a stalled codec, a crashed tab, a GPU-process crash mid-encode — and judges each by the app's own telemetry events. Not run in CI: headless Chrome encodes in software, where none of these exist. Needs ffmpeg/ffprobe on PATH.
