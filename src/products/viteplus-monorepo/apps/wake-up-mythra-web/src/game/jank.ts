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
// aggregate span, which makes every real player a feel canary. The ring is
// dev-only (`?probe=1`) and keeps the raw per-frame record, which is what a
// play-test harness needs to compute distributions a counter cannot carry.

/**
 * A frame is long past this gap: three missed 60Hz frames, which is where a
 * hitch stops being invisible.
 */
const LONG_FRAME_MS = 50;
/**
 * The park has stalled when the tick has not moved for this long while
 * frames keep rendering. At 24Hz that is six ticks — well past the jitter a
 * healthy clock absorbs, and about where a player says the park froze.
 */
const FREEZE_MS = 250;
/**
 * Own-dog displacement, in cells, that cannot be walking. A dog covers
 * roughly 0.05 cells per frame at 60fps, and the presentation smoother
 * snaps rather than interpolates past 8 cells; half a cell inside one frame
 * is a teleport in between — a rewind, a resync landing, or a snap.
 *
 * Only counted on frames that were not themselves long: after a hitch the
 * dog is legitimately somewhere else, and counting that would report the
 * hitch twice under two names.
 */
const JUMP_CELLS = 0.5;
/** Dogs the probe ring records per frame, our own aside. */
const PROBE_DOGS = 8;
/** Frames the probe ring holds: about 68 seconds at 60fps. */
const PROBE_CAP = 4096;
/**
 * Spans the probe ring holds. Frames are the what; these are the why, and
 * a leg that produces hundreds of them has a bigger problem than a ring
 * size.
 */
const SPAN_CAP = 256;
/** How often the aggregate leaves the page. */
const BEACON_MS = 60_000;

const Q16 = 65536;

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

/** One drawn frame, as the probe ring keeps it. Cells are Q16.16. */
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
  /** Flat id/x/y triples for up to PROBE_DOGS dogs; ids are hex. */
  readonly dogs: (string | number)[];
};

/** A span the app emitted, as the probe keeps it for a harness to read. */
export type SpanRecord = {
  readonly t: number;
  readonly name: string;
  readonly attrs: Record<string, string>;
};

/** What `?probe=1` publishes as `__mythraProbe` for a harness to read. */
export type Probe = {
  readonly cap: number;
  /** Frames overwritten before a drain. Nonzero means the harness is behind. */
  readonly dropped: number;
  /** Every held frame in order, oldest first, then empties the ring. */
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
   * Records a span the app emitted. Wired to the telemetry tap only under
   * `?probe=1`; a no-op otherwise, and never a second emission path.
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
  /** Keep the raw per-frame ring and publish it. Dev and harnesses only. */
  readonly probe: boolean;
  /** Where a minute's counters go. */
  readonly emit: (counters: JankCounters) => void;
}): Jank {
  let frames = 0;
  let longFrames = 0;
  let backwardTicks = 0;
  let ownDogJumps = 0;
  let freezeRuns = 0;

  let lastT = 0;
  let lastTick = -1;
  let tickChangedAt = 0;
  let stalled = false;
  let lastMineId: bigint | null = null;
  let lastMyX = 0;
  let lastMyY = 0;

  const ring: (FrameRecord | undefined)[] = opts.probe
    ? Array.from<undefined>({ length: PROBE_CAP })
    : [];
  let at = 0;
  let dropped = 0;
  let spans: SpanRecord[] = [];

  // Counters are cumulative for the life of the page, and the beacon
  // differences against its own last report to get a minute. A reader — the
  // probe, or a harness measuring one leg — can then difference over any
  // window it likes without racing the beacon's reset.
  const counters = (): JankCounters => ({
    frames,
    longFrames,
    backwardTicks,
    ownDogJumps,
    freezeRuns,
  });
  let flushed: JankCounters = counters();

  if (opts.probe) {
    const probe: Probe = {
      cap: PROBE_CAP,
      get dropped() {
        return dropped;
      },
      drain: () => {
        const out: FrameRecord[] = [];
        const held = Math.min(at, PROBE_CAP);
        const start = at - held;
        for (let i = start; i < at; i++) {
          const rec = ring[i % PROBE_CAP];
          if (rec) out.push(rec);
        }
        ring.fill(undefined);
        at = 0;
        dropped = 0;
        return out;
      },
      drainSpans: () => {
        const out = spans;
        spans = [];
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
      opts.emit({
        frames: now.frames - flushed.frames,
        longFrames: now.longFrames - flushed.longFrames,
        backwardTicks: now.backwardTicks - flushed.backwardTicks,
        ownDogJumps: now.ownDogJumps - flushed.ownDogJumps,
        freezeRuns: now.freezeRuns - flushed.freezeRuns,
      });
    }
    flushed = now;
  }, BEACON_MS);

  return {
    recordSpan: (name, attrs) => {
      if (!opts.probe) return;
      spans.push({ t: performance.now(), name, attrs });
      if (spans.length > SPAN_CAP) spans.splice(0, spans.length - SPAN_CAP);
    },
    sample: (t, tick, phaseQ16, mine, camX, camY, dogs) => {
      frames++;
      const gap = lastT === 0 ? 0 : t - lastT;
      const long = gap > LONG_FRAME_MS;
      if (long) longFrames++;

      if (tick < lastTick && lastTick >= 0) backwardTicks++;
      if (tick !== lastTick) {
        lastTick = tick;
        tickChangedAt = t;
        stalled = false;
      } else if (!stalled && tickChangedAt !== 0 && t - tickChangedAt > FREEZE_MS) {
        stalled = true;
        freezeRuns++;
      }

      if (mine) {
        if (!long && lastMineId === mine.id) {
          const dx = (mine.xq - lastMyX) / Q16;
          const dy = (mine.yq - lastMyY) / Q16;
          if (Math.hypot(dx, dy) > JUMP_CELLS) ownDogJumps++;
        }
        lastMineId = mine.id;
        lastMyX = mine.xq;
        lastMyY = mine.yq;
      } else {
        lastMineId = null;
      }
      lastT = t;

      if (!opts.probe) return;
      const kept: (string | number)[] = [];
      for (let i = 0; i < dogs.length && i < PROBE_DOGS; i++) {
        const dog = dogs[i]!;
        kept.push(dog.id.toString(16), dog.xq, dog.yq);
      }
      if (at >= PROBE_CAP && ring[at % PROBE_CAP]) dropped++;
      ring[at % PROBE_CAP] = {
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
      };
      at++;
    },
  };
}
