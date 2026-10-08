# anveio.com/cal: the contact-card prototype

The highest-fidelity version of the "Leave a message for Rumi" page: Shovon's iOS
dark-mode contact card on jet-black ink water, with Liquid Glass controls and
Rumi's orb. It's a plain Vite + React SPA with no backend; the flow is scripted.

## Run it

```sh
vp install                # once, from the repo root
vp run dev:cal            # from the repo root, or `vp dev` in this folder
open http://127.0.0.1:4255
```

You need a WebGPU browser (Chrome, Edge, Safari 26+). Without WebGPU, the glass
falls back to a frosted CSS surface and the ink to flat black.

## The loop

- **Studio** (right of the phone in dev; add `?studio` elsewhere): sliders and
  colours for Rumi, the Liquid Glass and the ink. Everything updates live.
  Your working draft survives reloads.
  - **Save as defaults** writes the draft into `src/tuning.ts` (dev server only).
  - **Copy as TS** copies it as a `const` you can paste anywhere.
  - **Revert to saved** throws the draft away.
- **Frames:** the studio's Frame menu (or `?frame=booked`, `?frame=host`, …)
  freezes the page on one step of Samantha's run. "Live prototype" plays the
  whole flow: tap Leave a message → Continue with Google → send → booked.
- **Shaders hot-reload:** edit any file in `src/shaders/` and the running
  scene recompiles in place without a page reload. A WGSL error shows in the
  studio with its file and line, while the last good shader keeps drawing.
- **Screenshots:** with the dev server up, `node scripts/shot.mjs arrive booked host`
  writes `shots/<frame>.png` plus a 1:1 close-up of the glass controls
  (`shots/<frame>-glass.png`).

## How it's drawn

| Layer (back to front) | What                                                                              |
| --------------------- | --------------------------------------------------------------------------------- |
| `layer-ink` canvas    | `ink.wgsl`: the swell plus tap ripples, rendered to a texture and shown           |
| `.scroll`             | the contact card: plain DOM, iOS type ramp and system colours (`styles.css`)      |
| `.underlay`           | backdrop-blur boxes under each glass shape, so the page blurs beneath the glass   |
| `layer-glass` canvas  | `glass.wgsl`: Liquid Glass optics for every `<Glass>` element, refracting the ink |
| `.controls`           | Rumi's bubble and the bar: the glass elements' content                            |

`<Glass radius=…>` registers its element. Each frame the scene reads every
glass box and draws them as one liquid-merged SDF, so neighbouring glass can
melt together (Studio → Liquid merge). The bezel refraction (Snell's law),
RGB dispersion, fresnel and glare come from
[liquid-glass-studio](https://github.com/iyinchao/liquid-glass-studio) (MIT,
vendored WGSL in `src/shaders/`, license alongside).

Rumi (`src/rumi/Orb.tsx` + `orb.wgsl`) is her own small WebGPU canvas. Her
states are `idle`, `listening`, `thinking` and `speaking`, and tapping her
mutes her.

Copy and timeline entries live in `src/content.ts`. Rumi's lines and Shovon's
note are verbatim, so don't paraphrase them.
