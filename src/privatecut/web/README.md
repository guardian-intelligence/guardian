# PrivateCut — rumi.engineering

Browser-native video clipper: mediabunny in a worker cuts up to a minute of any video at the best quality that fits under a hard size cap, gated by measured output size — nothing uploads. TanStack Start (React, SSR on nitro) + Tailwind, with a WebGL2 canvas compositor that progressively enables native HTML-in-canvas; ships as a Bazel-built OCI image.

Dev loop: `vp install && vp dev`, then `vp run ready` as the pre-merge gate (lint + test + typecheck + build).

Visual testing: `packages/visual-harness` captures deterministic frames and gates drift against committed baselines — see its README for local capture and baseline refresh.
