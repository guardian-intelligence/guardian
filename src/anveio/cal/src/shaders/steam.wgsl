// Rumi's steam: two coloured gases (green and teal) in slowly moving air, on a
// small wrap-around grid, simulated as an incompressible fluid (Stam's
// "stable fluids"). Each frame steam.ts runs, in order:
//
//   fs_velocity    the air carries itself along (semi-Lagrangian advection),
//                  settles toward a gentle wind, rises where the gas is (steam
//                  is warm), and is stirred by a slow swirl so it never stills
//   fs_divergence  how much the air is bunching up or thinning out
//   fs_jacobi      relaxes the pressure that would undo that (run many times)
//   fs_project     pushes the air down the pressure gradient, so it stays
//                  incompressible: it curls and billows instead of piling up
//   fs_gas         the gases ride the air, diffuse into it, fade, and are fed
//                  by a few slowly drifting sources
//
// Every pass reads up to two textures (a, b) and writes one; all are rgba16f.
// Velocities are in grid uv per second.

struct Sim {
  grid: vec4f,   // texel size (uv), dt (s), time (s)
  wind: vec4f,   // wind (uv/s) x, y, how fast the air settles to it (1/s), buoyancy
  look: vec4f,   // gas diffusion (1/s), gas fading (1/s), swirl strength, unused
  src0: vec4f,   // a green source: x, y (uv), radius (uv), strength (thickness/s)
  src1: vec4f,   // a teal source
  src2: vec4f,   // green
  src3: vec4f,   // teal
}
@group(0) @binding(0) var<uniform> u: Sim;
@group(0) @binding(1) var a: texture_2d<f32>;
@group(0) @binding(2) var b: texture_2d<f32>;
@group(0) @binding(3) var wrap: sampler;

fn hash2(p: vec2f) -> f32 {
  return fract(sin(dot(p, vec2f(127.1, 311.7))) * 43758.5453);
}

// tileable value noise with period n (so the swirl wraps with the grid)
fn noise(p: vec2f, n: f32) -> f32 {
  let i = floor(p);
  let f = fract(p);
  let w = f * f * (3.0 - 2.0 * f);
  let c00 = hash2(i - n * floor(i / n));
  let i10 = i + vec2f(1.0, 0.0);
  let i01 = i + vec2f(0.0, 1.0);
  let i11 = i + vec2f(1.0, 1.0);
  let c10 = hash2(i10 - n * floor(i10 / n));
  let c01 = hash2(i01 - n * floor(i01 / n));
  let c11 = hash2(i11 - n * floor(i11 / n));
  return mix(mix(c00, c10, w.x), mix(c01, c11, w.x), w.y);
}

fn at(t: texture_2d<f32>, uv: vec2f) -> vec4f {
  return textureSampleLevel(t, wrap, uv, 0.0);
}

// A slow stirring swirl: the curl of a drifting noise potential, so it adds
// eddies without adding or removing any air.
fn swirl(uv: vec2f) -> vec2f {
  let n = 6.0;
  let e = 0.02;
  let s = uv * n + vec2f(u.grid.w * 0.05, -u.grid.w * 0.04);
  let dx = noise(s + vec2f(e, 0.0), n) - noise(s - vec2f(e, 0.0), n);
  let dy = noise(s + vec2f(0.0, e), n) - noise(s - vec2f(0.0, e), n);
  return vec2f(dy, -dx) / (2.0 * e);
}

@fragment
fn fs_velocity(@location(0) uv: vec2f) -> @location(0) vec4f {
  let dt = u.grid.z;
  var v = at(a, uv - dt * at(a, uv).xy).xy;
  v += (u.wind.xy - v) * min(1.0, dt * u.wind.z);
  let gas = at(b, uv);
  // steam rises (uv y runs down)
  v.y -= u.wind.w * (gas.r + gas.g) * dt;
  v += swirl(uv) * u.look.z * dt;
  return vec4f(v, 0.0, 1.0);
}

@fragment
fn fs_divergence(@location(0) uv: vec2f) -> @location(0) vec4f {
  let h = u.grid.x;
  let l = at(a, uv - vec2f(h, 0.0)).x;
  let r = at(a, uv + vec2f(h, 0.0)).x;
  let t = at(a, uv - vec2f(0.0, h)).y;
  let d = at(a, uv + vec2f(0.0, h)).y;
  return vec4f(((r - l) + (d - t)) / (2.0 * h), 0.0, 0.0, 1.0);
}

// a = pressure, b = divergence
@fragment
fn fs_jacobi(@location(0) uv: vec2f) -> @location(0) vec4f {
  let h = u.grid.x;
  let l = at(a, uv - vec2f(h, 0.0)).x;
  let r = at(a, uv + vec2f(h, 0.0)).x;
  let t = at(a, uv - vec2f(0.0, h)).x;
  let d = at(a, uv + vec2f(0.0, h)).x;
  let div = at(b, uv).x;
  return vec4f((l + r + t + d - h * h * div) * 0.25, 0.0, 0.0, 1.0);
}

// a = velocity, b = pressure
@fragment
fn fs_project(@location(0) uv: vec2f) -> @location(0) vec4f {
  let h = u.grid.x;
  let l = at(b, uv - vec2f(h, 0.0)).x;
  let r = at(b, uv + vec2f(h, 0.0)).x;
  let t = at(b, uv - vec2f(0.0, h)).x;
  let d = at(b, uv + vec2f(0.0, h)).x;
  let v = at(a, uv).xy - vec2f(r - l, d - t) / (2.0 * h);
  return vec4f(v, 0.0, 1.0);
}

// how much a source at s (x, y, radius, strength) feeds the point uv, wrapping
fn feed(uv: vec2f, s: vec4f) -> f32 {
  let d = fract(uv - s.xy + 0.5) - 0.5;
  return s.w * exp(-dot(d, d) / (s.z * s.z));
}

// a = gas (r green, g teal), b = velocity
@fragment
fn fs_gas(@location(0) uv: vec2f) -> @location(0) vec4f {
  let dt = u.grid.z;
  let h = u.grid.x;
  let back = uv - dt * at(b, uv).xy;
  var gas = at(a, back).rg;
  // diffusion: blend toward the neighbourhood's mean
  let around = (at(a, back + vec2f(h, 0.0)).rg + at(a, back - vec2f(h, 0.0)).rg
    + at(a, back + vec2f(0.0, h)).rg + at(a, back - vec2f(0.0, h)).rg) * 0.25;
  gas = mix(gas, around, min(1.0, u.look.x * dt));
  gas *= 1.0 - min(1.0, u.look.y * dt);
  gas += vec2f(feed(uv, u.src0) + feed(uv, u.src2), feed(uv, u.src1) + feed(uv, u.src3)) * dt;
  return vec4f(clamp(gas, vec2f(0.0), vec2f(4.0)), 0.0, 1.0);
}
