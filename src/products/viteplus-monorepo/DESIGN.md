# Vite+ workspace design invariants

This file defines visual rules shared by the public Guardian surfaces in this
workspace. Product-specific treatments may add constraints, but they must not
silently redefine these measurements.

## Badge lockups

A **badge** is a discrete square, circular, or rounded container carrying the
Guardian wings without a wordmark. App icons, favicons, social avatars, and
framed identity marks are badges. Bare wings, wordmark lockups, illustrations,
and the translucent hero orb are not badges.

### Reference occupancy

The badge proportion is derived from the supplied Ramp reference raster:

| Measurement            |     Pixels | Canvas ratio |
| ---------------------- | ---------: | -----------: |
| Canvas                 |  400 × 400 |  100% × 100% |
| Dark-mark bounding box |  184 × 160 |    46% × 40% |
| Bounding-box area      | 29,440 px² |    **18.4%** |
| Equivalent square side |          — |      42.895% |

The raster's dominant background is RGB `228 242 33` and its dominant mark is
RGB `28 27 23`. The recorded box uses the dark foreground core at the
midpoint threshold and excludes the antialias fringe. Prefer vector path
bounds when source artwork exists; when measuring raster references, record
the threshold with the result.

The canonical invariant is the **18.4% foreground bounding-box area**, not
either reference dimension in isolation. Matching area lets marks with
different aspect ratios carry the same geometric presence without stretching
their silhouettes.

For a badge canvas with side `B`, a mark with width-to-height aspect ratio `a`,
and the reference occupancy `R = 0.184`:

```text
mark height / B = sqrt(R / a)
mark width  / B = sqrt(R * a)
```

The Guardian wings have a source bounding box of 102.174 × 120.823 units, so
`a = 0.845650`. A Guardian badge therefore uses:

```text
wing width  = 39.446% of B
wing height = 46.646% of B
wing bounding-box area = 18.4% of B²
```

Center the resulting foreground bounding box in the badge canvas. Optical
translation is allowed when a mark demonstrably reads off-center, but it must
not change the occupancy ratio. Never distort the mark to copy the Ramp
reference's width and height independently.

### Canvas versus placement

The occupancy ratio begins only after the badge canvas has been defined.

- The **placement slot** is the layout space reserved for the badge.
- The **badge canvas** is an explicit square inside that slot.
- The **foreground bounding box** is measured from visible mark pixels inside
  the badge canvas.
- Shadows, glows, focus rings, touch targets, and ambient light fields sit
  outside the badge canvas and do not participate in the ratio.

For an operating-system icon, the exported image is the badge canvas. For a
badge on a larger same-colour surface, use the component's explicit square
icon slot as the canvas; do not use the page, card, or viewport as an implied
canvas. Size and align that outer square according to the surrounding layout,
then apply the invariant internally.

Badge backgrounds are full-bleed and opaque. Platform masks supply the final
corner radius or silhouette. Do not pre-round app-icon artwork, and do not add
extra mask-specific scaling when the invariant already fits inside the
platform safe zone.

### Shortty

The Shortty badge at `rumi.engineering` uses Ink `#0a0a0e` as the full canvas
and Argent `#e8e6f0` for the wings. The same 18.4% occupancy applies to:

- `favicon.svg`
- `apple-touch-icon.png`
- `icon-192.png`
- `icon-512.png`
- `icon-maskable.svg`
- `icon-maskable-512.png`

The standard and maskable Android files remain separate manifest entries, but
their internal geometry is identical. Platform masking changes the outer
silhouette, not the Guardian mark's scale.

`apps/shortty-web/src/lib/platform-icons.test.ts` is the executable contract:
it checks the SVG transform, exported PNG foreground bounds, opaque
background, dimensions, and manifest purposes.
