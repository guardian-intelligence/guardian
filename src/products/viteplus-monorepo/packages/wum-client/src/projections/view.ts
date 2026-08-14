// The `sim_view` projection — the park as a renderer reads it — and the
// glide presenter that carries the screen across a correction. Game
// bytes, game rules: none of this belongs in the replica host.

import type { TerrainPlanes } from "../terrain.ts";

/** Q16.16, the fixed point the view and the sim share. */
export const Q16 = 65536;

/** Bytes per dog in the `sim_view` projection (after the leading u32 count). */
export const VIEW_RECORD_BYTES = 20;

export type ViewDog = {
  readonly id: bigint;
  /** Q16.16 park cells. */
  readonly x: number;
  readonly y: number;
  /** bit0 deck, bit1 swimming, bit2 moving, bit3 boosting. */
  readonly flags: number;
  /** Octant, 0=E clockwise; 2 (south) when idle. */
  readonly facing: number;
  readonly anim: number;
};

/** Reads dog record `index` of a `sim_view` projection. */
export function decodeViewDog(bytes: Uint8Array, index: number): ViewDog {
  const dv = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
  const at = 4 + index * VIEW_RECORD_BYTES;
  return {
    id: dv.getBigUint64(at, true),
    x: dv.getInt32(at + 8, true),
    y: dv.getInt32(at + 12, true),
    flags: dv.getUint8(at + 16),
    facing: dv.getUint8(at + 17),
    anim: dv.getUint8(at + 18),
  };
}

/** What a renderer needs per frame. Valid until the next `frame()` call. */
export type FrameView = {
  /** The `sim_view` projection: u32 count, then 20-byte dog records, glide applied. */
  readonly viewBytes: Uint8Array;
  readonly terrain: TerrainPlanes | null;
  /** Q16 phase between ticks, for interpolation. */
  readonly phaseQ16: number;
  readonly tick: bigint;
};

/**
 * How long the presenter takes to absorb a re-derivation. The presented
 * world is the journal plus our unanswered intents, and re-deriving it is
 * how the two are kept from drifting apart — but a re-derivation moves
 * dogs without time passing, which is the one thing a renderer cannot
 * interpolate through. So the presenter keeps showing them where they
 * were and closes the gap over this window.
 */
const GLIDE_MS = 150;

/**
 * The fastest the presenter will move a dog to close that gap, in cells
 * per second. A dog walks three, so this is a correction hurrying visibly
 * without becoming a teleport — and it is a RATE rather than a per-frame
 * step, so the bound holds at any refresh rate rather than only at the one
 * it was tuned on. A gap too large for `GLIDE_MS` at this speed takes
 * longer instead of moving faster.
 *
 * Twelve keeps the worst frame under a third of a cell on a 30Hz display,
 * which is the slowest refresh worth designing for; a faster display only
 * makes the same motion smoother.
 */
export const GLIDE_MAX_CELLS_PER_SEC = 12;

/**
 * The largest gap the presenter will close on the screen's behalf, in
 * cells. A re-derivation costs at most about a round trip of walking, so
 * anything past this is not a correction being hidden — it is a world
 * change, and gliding a dog across one pretends a continuity that did not
 * happen. Beyond the cap the dog simply arrives where the world says it
 * is, which is the honest picture of a world that really did change.
 */
export const GLIDE_MAX_CELLS = 4;

/** Re-check for departed dogs about every four seconds of a 60Hz loop. */
const GLIDE_PRUNE_FRAMES = 256;

/** Smoothstep's peak slope: the fastest moment of a glide, relative to a linear one. */
const GLIDE_PEAK_SLOPE = 1.5;

/**
 * How long a correction of `cells` may take. Small ones take `GLIDE_MS`;
 * larger ones stretch, so the dog never crosses the gap faster than
 * `GLIDE_MAX_CELLS_PER_SEC` at the steepest point of the curve. Stretching
 * rather than capping means there is no cliff at any particular size — a
 * correction one cell bigger looks one cell bigger, not suddenly instant.
 */
function glideWindowMs(cells: number): number {
  const atFullSpeed = (GLIDE_PEAK_SLOPE * cells * 1000) / GLIDE_MAX_CELLS_PER_SEC;
  return Math.max(GLIDE_MS, atFullSpeed);
}

/**
 * How much of a glide is still owed after `elapsedMs` of its `windowMs`.
 * Smooth at both ends, so neither taking the glide on nor finishing it is
 * a kink in the dog's motion.
 */
function glideRemaining(elapsedMs: number, windowMs: number): number {
  if (elapsedMs <= 0) return 1;
  if (elapsedMs >= windowMs) return 0;
  const u = elapsedMs / windowMs;
  return 1 - u * u * (3 - 2 * u);
}

/**
 * Carries the screen across re-derivations. On the frame after one, each
 * dog is owed the difference between where it was last shown and where
 * the re-derived world puts it; that debt is paid off over the glide
 * window, so what a viewer sees is continuous even though the world
 * underneath moved without time passing.
 *
 * Only the view copy handed to `apply` is edited — never the world. The
 * replica instance keeps whatever the session put in it, so hashes and
 * checks are untouched by anything here.
 */
export class GlidePresenter {
  /** Where each dog was last shown, in Q16 view units, and when. */
  readonly #shown = new Map<bigint, { x: number; y: number; frame: number }>();
  /** What a re-derivation owes each dog, in Q16 view units, and since when. */
  readonly #glide = new Map<bigint, { dx: number; dy: number; sinceMs: number; ms: number }>();
  #frame = 0;

  /**
   * Runs the presenter over one frame's view copy, in place. `corrected`
   * says a pump re-derived the world since the last frame a viewer saw —
   * that frame's gap is what the glide is owed from.
   */
  apply(out: Uint8Array, nowMs: number, corrected: boolean): void {
    const dv = new DataView(out.buffer, out.byteOffset, out.byteLength);
    const n = dv.getUint32(0, true);
    const frame = ++this.#frame;

    if (corrected) {
      for (let i = 0; i < n; i++) {
        const at = 4 + i * VIEW_RECORD_BYTES;
        const was = this.#shown.get(dv.getBigUint64(at, true));
        // A dog nobody has seen yet has no continuity to keep.
        if (!was) continue;
        const dx = was.x - dv.getInt32(at + 8, true);
        const dy = was.y - dv.getInt32(at + 12, true);
        if (dx === 0 && dy === 0) continue;
        // Measured as a distance, not per axis: a correction that is three
        // cells north AND three cells east is a five-cell correction, and
        // capping the axes would let it through as two small ones.
        const cells = Math.hypot(dx, dy) / Q16;
        if (cells > GLIDE_MAX_CELLS) continue;
        const ms = glideWindowMs(cells);
        this.#glide.set(dv.getBigUint64(at, true), { dx, dy, sinceMs: nowMs, ms });
      }
    }

    for (let i = 0; i < n; i++) {
      const at = 4 + i * VIEW_RECORD_BYTES;
      const id = dv.getBigUint64(at, true);
      let x = dv.getInt32(at + 8, true);
      let y = dv.getInt32(at + 12, true);
      const owed = this.#glide.get(id);
      if (owed) {
        const left = glideRemaining(nowMs - owed.sinceMs, owed.ms);
        if (left <= 0) this.#glide.delete(id);
        else {
          x += Math.round(owed.dx * left);
          y += Math.round(owed.dy * left);
          dv.setInt32(at + 8, x, true);
          dv.setInt32(at + 12, y, true);
        }
      }
      const seen = this.#shown.get(id);
      if (seen) {
        seen.x = x;
        seen.y = y;
        seen.frame = frame;
      } else {
        this.#shown.set(id, { x, y, frame });
      }
    }

    // Dogs that have left the park stop being anyone's continuity.
    if (frame % GLIDE_PRUNE_FRAMES === 0) {
      for (const [id, seen] of this.#shown) {
        if (seen.frame === frame) continue;
        this.#shown.delete(id);
        this.#glide.delete(id);
      }
    }
  }

  /**
   * How much correction the presenter is still carrying, in cells, summed
   * over every dog. Zero means the view is showing the world exactly as
   * the session holds it.
   *
   * This exists to state the invariant that the glide is TEMPORARY. A
   * presenter that never pays its debt off shows a wrong position
   * forever, and every continuity gate in the suite would pass while it
   * did.
   */
  debtCells(nowMs: number): number {
    let owed = 0;
    for (const g of this.#glide.values()) {
      const left = glideRemaining(nowMs - g.sinceMs, g.ms);
      owed += (Math.abs(g.dx) + Math.abs(g.dy)) * left;
    }
    return owed / Q16;
  }
}
