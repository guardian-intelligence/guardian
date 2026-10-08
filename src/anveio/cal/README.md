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

- **Design values:** the colours and dials for Rumi, the Liquid Glass and the
  ink are plain constants in `src/design.ts`; Vite picks up edits to them.
- **Frames:** `?frame=booked`, `?frame=host`, … freezes the page on one step of
  Samantha's run. Without `?frame=` the page plays the whole flow: tap Leave a
  message → Continue with Google → send → booked.
- **Shaders hot-reload:** edit any file in `src/shaders/` and the running
  scene recompiles in place without a page reload. A WGSL error is logged to
  the browser console with its file and line, while the last good shader keeps
  drawing.
- **Screenshots:** with the dev server up, `node scripts/shot.mjs arrive booked host`
  writes `shots/<frame>.png` plus a 1:1 close-up of the glass controls
  (`shots/<frame>-glass.png`).

## How it's drawn

| Layer (back to front) | What                                                                              |
| --------------------- | --------------------------------------------------------------------------------- |
| `layer-ink` canvas    | `ink.wgsl`: the swell and Rumi's voice waves, rendered to a texture and shown     |
| `.scroll`             | the contact card: plain DOM, iOS type ramp and system colours (`styles.css`)      |
| `.underlay`           | backdrop-blur boxes under each glass shape, so the page blurs beneath the glass   |
| `layer-glass` canvas  | `glass.wgsl`: Liquid Glass optics for every `<Glass>` element, refracting the ink |
| `.controls`           | Rumi's bubble and the bar: the glass elements' content                            |

`<Glass radius=…>` registers its element. Each frame the scene reads every
glass box and draws them as one liquid-merged SDF, so neighbouring glass can
melt together (`glass.mergeRate` in `src/design.ts`). The bezel refraction (Snell's law),
RGB dispersion, fresnel and glare come from
[liquid-glass-studio](https://github.com/iyinchao/liquid-glass-studio) (MIT,
vendored WGSL in `src/shaders/`, license alongside).

Rumi (`src/rumi/Orb.tsx` + `orb.wgsl`) is her own small WebGPU canvas: a
microscope's view of two coloured gases, green and teal, drifting and diffusing
like steam inside her, lit from behind. The steam is a real fluid simulation
(`src/rumi/steam.ts` + `steam.wgsl`, Stam's stable fluids on a 96x96
wrap-around grid): the air carries itself, settles toward a gentle wind,
rises where the gas is warm, and stays incompressible, so it curls and
billows; the gases ride it, diffuse and fade, fed by four drifting sources.
Each gas absorbs light by thickness (Beer-Lambert), so dense plumes deepen and
thin wisps glow, and a fine film grain sits over everything. Colours, the wind
and the steam's physics live in `DESIGN.orb` (`src/design.ts`). Her states are
`idle`, `listening` (focus pulls in), `thinking` (stronger wind) and
`speaking` (a soft brightness pulse); tapping her mutes her. With reduced
motion the steam holds still and only the focus breathes; `public/rumi.png`
stands in for her without WebGPU. `?lab` (with `&mode=` and `&size=`) shows her
alone at 640px.

She is also the page's light. Each frame `orb.wgsl`'s `fs_emission` raymarches
her from the far wall behind her into a 64-texel strip, one texel per direction
around her (`src/rumi/emission.ts`). `ink.wgsl` samples it for the tight halo
her light leaves on the wall, and a CPU copy colours the reflection on every
glass element (`lightGlass` in `src/glass/Glass.tsx`, painted by `.glass::after`).

Copy and timeline entries live in `src/content.ts`. Rumi's lines and Shovon's
note are verbatim, so don't paraphrase them.
