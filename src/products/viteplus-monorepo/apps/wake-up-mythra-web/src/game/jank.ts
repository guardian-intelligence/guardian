// Feel, as numbers.
//
// The correctness suites can prove the replica agrees with the authority
// tick for tick and still miss a session that is miserable to play: the
// world rewinds, our own dog teleports, the park stalls for a second. Those
// are properties of the frames the renderer actually drew, so that is where
// they are measured.
//
// One set of definitions, two consumers. The counters run always — a
// handful of comparisons per frame — and leave once a minute as an
// aggregate span, which makes every real player a feel canary. The buffers
// are dev-only (`?probe=1`) and keep the raw per-frame record, which is
// what a play-test harness needs to compute distributions a counter cannot
// carry.

import { Q16 } from "@guardian/mythrad-client-core";

/**
 * A frame is long past this gap: three missed 60Hz frames, which is where a
 * hitch stops being invisible.
 */
const LONG_FRAME_MS = 50;
/**
 * The park has stalled when the tick has not moved for this long while
 * frames keep rendering. A purely perceptual threshold: a quarter second of
 * the world standing still is about where a player says the park froze.
 */
const FREEZE_MS = 250;
/**
 * Own-dog displacement, in cells, that cannot be walking: half a cell
 * inside one frame is a teleport — a rewind, a resync landing, or a snap
 * from either presentation layer (the glide presenter gives up past
 * `GLIDE_MAX_CELLS`, the smoother past the renderer's snap distance).
 *
 * Only counted on frames that were not themselves long: after a hitch the
 * dog is legitimately somewhere else, and counting that would report the
 * hitch twice under two names.
 */
const JUMP_CELLS = 0.5;
const JUMP_Q16_SQ = (JUMP_CELLS * Q16) ** 2;
/** Dogs the probe records per frame, our own aside. */
const PROBE_DOGS = 8;
/** Frames the probe holds: about 68 seconds at 60fps. */
const PROBE_CAP = 4096;
/**
 * Spans the probe holds. Frames are the what; these are the why.
 *
 * Sized for a leg that emits one span per journalled event — around 140 —
 * plus its repairs, with room to spare. Overflow drops the OLDEST, which
 * is exactly the wrong end: a leg's first repair is usually the one that
 * explains the rest. So it is counted rather than silent, and a reader
 * that sees a non-zero count knows its earliest evidence is missing.
 */
const SPAN_CAP = 1024;
/** How often the aggregate leaves the page. */
const BEACON_MS = 60_000;

/** Enough of a dog to place it. The renderer's view records satisfy this. */
export type DogPos = {
  readonly id: bigint;
  readonly xq: number;
  readonly yq: number;
};

export type JankCounters = {
  readonly frames: number;
  readonly longFrames: number;
  readonly backwardTicks: number;
  readonly ownDogJumps: number;
  readonly freezeRuns: number;
};

/** One drawn frame, as the probe keeps it. Cells are Q16.16. */
export type FrameRecord = {
  readonly t: number;
  readonly tick: number;
  readonly phaseQ16: number;
  readonly mine: boolean;
  readonly myX: number;
  readonly myY: number;
  readonly camX: number;
  readonly camY: number;
  readonly dogCount: number;
  /** Up to PROBE_DOGS dogs; ids are hex. */
  readonly dogs: readonly { id: string; xq: number; yq: number }[];
};

/** A span the app emitted, as the probe keeps it for a harness to read. */
export type SpanRecord = {
  readonly t: number;
  readonly name: string;
  readonly attrs: Record<string, string>;
};

/** What `?probe=1` publishes as `__mythraProbe` for a harness to read. */
export type Probe = {
  /** Frames dropped to overflow before a drain. Nonzero means the harness is behind. */
  readonly dropped: number;
  /** Spans lost to overflow since the last drain. Non-zero means the oldest are gone. */
  readonly droppedSpans: number;
  /** Every held frame in order, oldest first, then empties the buffer. */
  drain(): FrameRecord[];
  /**
   * Every span the app emitted since the last call, oldest first. The
   * frames say a rewind happened; these say which repair asked for it.
   */
  drainSpans(): SpanRecord[];
  /**
   * The counters behind the beacon, cumulative since the page loaded. The
   * beacon reports differences of these, so a harness that differences them
   * over a leg is checking the exact numbers production reports.
   */
  counters(): JankCounters;
};

export type Jank = {
  /**
   * Records a span the app emitted. The composition root wires it to the
   * telemetry tap only under `?probe=1`, and it is never a second
   * emission path.
   */
  readonly recordSpan: (name: string, attrs: Record<string, string>) => void;
  /** Called once per drawn frame, from the render loop. */
  readonly sample: (
    t: number,
    tick: number,
    phaseQ16: number,
    mine: DogPos | null,
    camX: number,
    camY: number,
    dogs: readonly DogPos[],
  ) => void;
};

export function createJank(opts: {
  /** Keep the raw per-frame buffer and publish it. Dev and harnesses only. */
  readonly probe: boolean;
  /** Where a minute's counters go. */
  readonly emit: (counters: JankCounters) => void;
}): Jank {
  const c: { -readonly [K in keyof JankCounters]: number } = {
    frames: 0,
    longFrames: 0,
    backwardTicks: 0,
    ownDogJumps: 0,
    freezeRuns: 0,
  };

  let lastT = 0;
  let lastTick = -1;
  let tickChangedAt = 0;
  let lastMineId: bigint | null = null;
  let lastMyX = 0;
  let lastMyY = 0;

  let held: FrameRecord[] = [];
  let dropped = 0;
  let spans: SpanRecord[] = [];
  let droppedSpans = 0;

  // Counters are cumulative for the life of the page; every reader — the
  // beacon, the probe, a harness measuring one leg — differences them over
  // its own window, so none races another's reset.
  const counters = (): JankCounters => ({ ...c });
  let flushed = counters();

  if (opts.probe) {
    const probe: Probe = {
      get dropped() {
        return dropped;
      },
      get droppedSpans() {
        return droppedSpans;
      },
      drain: () => {
        const out = held;
        held = [];
        dropped = 0;
        return out;
      },
      drainSpans: () => {
        const out = spans;
        spans = [];
        droppedSpans = 0;
        return out;
      },
      counters,
    };
    Object.assign(globalThis, { __mythraProbe: probe });
  }

  setInterval(() => {
    const now = counters();
    // A page that drew nothing this minute — a background tab — has no feel
    // to report, and a beacon of zeros from it would only dilute the ones
    // that do.
    if (now.frames > flushed.frames) {
      const minute = {} as { -readonly [K in keyof JankCounters]: number };
      for (const k of Object.keys(now) as (keyof JankCounters)[]) {
        minute[k] = now[k] - flushed[k];
      }
      opts.emit(minute);
    }
    flushed = now;
  }, BEACON_MS);

  return {
    recordSpan: (name, attrs) => {
      spans.push({ t: performance.now(), name, attrs });
      if (spans.length > SPAN_CAP) {
        spans.shift();
        droppedSpans++;
      }
    },
    sample: (t, tick, phaseQ16, mine, camX, camY, dogs) => {
      c.frames++;
      const long = lastT !== 0 && t - lastT > LONG_FRAME_MS;
      if (long) c.longFrames++;

      if (tick < lastTick && lastTick >= 0) c.backwardTicks++;
      if (tick !== lastTick) {
        lastTick = tick;
        tickChangedAt = t;
      } else if (t - tickChangedAt > FREEZE_MS) {
        c.freezeRuns++;
        // One count per stall run: park here until the tick moves again.
        tickChangedAt = Infinity;
      }

      if (mine) {
        if (!long && lastMineId === mine.id) {
          const dx = mine.xq - lastMyX;
          const dy = mine.yq - lastMyY;
          if (dx * dx + dy * dy > JUMP_Q16_SQ) c.ownDogJumps++;
        }
        lastMineId = mine.id;
        lastMyX = mine.xq;
        lastMyY = mine.yq;
      } else {
        lastMineId = null;
      }
      lastT = t;

      if (!opts.probe) return;
      const kept: { id: string; xq: number; yq: number }[] = [];
      for (let i = 0; i < dogs.length && i < PROBE_DOGS; i++) {
        const dog = dogs[i]!;
        kept.push({ id: dog.id.toString(16), xq: dog.xq, yq: dog.yq });
      }
      held.push({
        t,
        tick,
        phaseQ16,
        mine: mine !== null,
        myX: mine ? mine.xq : 0,
        myY: mine ? mine.yq : 0,
        camX,
        camY,
        dogCount: dogs.length,
        dogs: kept,
      });
      if (held.length > PROBE_CAP) {
        held.shift();
        dropped++;
      }
    },
  };
}
