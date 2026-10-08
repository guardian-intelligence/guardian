// Rumi's light as a candle flame: never periodic, never uniform.
//
//   brightness = 1 + slow waxing/waning + fine flicker - occasional gust dips
//   size       follows the slow waxing (the flame grows as it brightens)
//   sway       the flame's lean, a slow wander of where the light falls
//
// Every term is smooth (cubic value noise or eased envelopes), so there are no
// single-frame pops; layering incommensurate rates keeps it from repeating.
// scripts/measure-flame.mjs checks these properties against the live page.

export type FlameTuning = {
  /** depth of the slow waxing and waning, as a fraction of the mean */
  waver: number;
  /** depth of the fine flicker */
  flicker: number;
  /** overall tempo multiplier */
  rate: number;
  /** strength of the occasional gust that briefly dims the flame */
  gust: number;
};

export type FlameState = { brightness: number; size: number; swayX: number; swayY: number };

function mulberry32(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/** 1-D value noise in [-1, 1], C1-smooth (quintic fade). */
function valueNoise(seed: number): (x: number) => number {
  const rand = mulberry32(seed);
  const table = Float32Array.from({ length: 512 }, () => rand() * 2 - 1);
  return (x: number) => {
    const i = Math.floor(x);
    const f = x - i;
    const u = f * f * f * (f * (f * 6 - 15) + 10);
    const a = table[i & 511] ?? 0;
    const b = table[(i + 1) & 511] ?? 0;
    return a + (b - a) * u;
  };
}

export function createFlame(seed = 7) {
  const octave = (k: number) => valueNoise(seed * 101 + k * 7919);
  const [n0, n1, n2, n3, n4, n5, n6] = [
    octave(0),
    octave(1),
    octave(2),
    octave(3),
    octave(4),
    octave(5),
    octave(6),
  ] as const;
  const rand = mulberry32(seed * 31 + 3);
  let t = 0;
  let nextGust = 3 + rand() * 5;
  let gustAt = -10;
  let gustDepth = 0;

  return (dt: number, tune: FlameTuning): FlameState => {
    t += dt * tune.rate;

    // slow waxing and waning: two incommensurate octaves (~0.15 and ~0.37 Hz)
    const wax = 0.65 * n0(t * 0.15) + 0.35 * n1(t * 0.37 + 11.3);
    // fine flicker (~1.4 and ~2.2 Hz), its depth itself breathing with the slow layer
    const flick = (0.6 * n2(t * 1.4) + 0.4 * n3(t * 2.2 + 5.1)) * (0.7 + 0.3 * n4(t * 0.11));

    // gusts: every 3-8 s the flame ducks (eased in over 0.6 s) and slowly recovers
    if (t >= nextGust) {
      gustAt = t;
      gustDepth = 0.5 + rand() * 0.5;
      nextGust = t + 3 + rand() * 5;
    }
    const age = t - gustAt;
    const gustEnv =
      age < 0.6 ? 0.5 - 0.5 * Math.cos((age / 0.6) * Math.PI) : Math.exp(-(age - 0.6) / 0.8);
    const gust = age >= 0 && age < 4 ? gustEnv * gustDepth * tune.gust : 0;

    const brightness = Math.max(0.05, 1 + tune.waver * wax + tune.flicker * flick - gust);
    const size = Math.max(
      0.3,
      1 + 0.8 * tune.waver * wax + 0.25 * tune.flicker * flick - 0.4 * gust,
    );
    return {
      brightness,
      size,
      swayX: n5(t * 0.45 + 2.2) * 0.5 + gust * 0.6,
      swayY: n6(t * 0.33 + 9.7),
    };
  };
}
