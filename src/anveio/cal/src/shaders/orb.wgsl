// Rumi: pigments of the Bangladesh flag and Bengal indigo, smeared and swirled
// like wet paint inside a sphere of Liquid Glass, lit from within by a candle.
//
//   paint        three pigment fields, domain-warped so they smear into blobs
//                and get stirred around; how they move is Rumi's state
//   glass        the paint is seen through the sphere: blurred (frosted), bent
//                and colour-split at the rim (Snell's law, as glass.wgsl)
//   luminescence a candle glow inside her that waxes, wanes, flickers and
//                sways (src/rumi/flame.ts), tinting the paint it lights

struct Params {
  frame: vec4f,  // canvas px x, y, time (s), matte 0..1
  motion: vec4f, // paint time (s, state-paced), swirl angle (rad), wave phase (rad), wave amplitude
  look: vec4f,   // smear scale, warp, blob sharpness, mute 0..1
  green: vec4f,  // rgb, amount bias
  red: vec4f,    // rgb, amount bias
  indigo: vec4f, // rgb, amount bias
  glow: vec4f,   // rgb, glow strength
  flame: vec4f,  // brightness, size, sway x, sway y
  glass: vec4f,  // blur (lens units), bezel width (lens units), refractive index, dispersion
  shine: vec4f,  // glare, fresnel, glow radius, unused
}
@group(0) @binding(0) var<uniform> u: Params;

const VIEW: f32 = 1.12;

fn toLinear(c: vec3f) -> vec3f { return pow(c, vec3f(2.2)); }

fn hash(p: vec2f) -> f32 {
  return fract(sin(dot(p, vec2f(127.1, 311.7))) * 43758.5453);
}

fn noise(p: vec2f) -> f32 {
  let i = floor(p);
  let f = fract(p);
  let w = f * f * f * (f * (f * 6.0 - 15.0) + 10.0);
  return mix(mix(hash(i), hash(i + vec2f(1.0, 0.0)), w.x),
             mix(hash(i + vec2f(0.0, 1.0)), hash(i + vec2f(1.0, 1.0)), w.x), w.y);
}

fn fbm(p_in: vec2f) -> f32 {
  var p = p_in;
  var a = 0.5;
  var s = 0.0;
  for (var i = 0; i < 4; i = i + 1) {
    s += a * noise(p);
    p = p * 2.03 + vec2f(17.1, 9.2);
    a *= 0.5;
  }
  return s;
}

fn rot(a: f32) -> mat2x2f {
  let c = cos(a); let s = sin(a);
  return mat2x2f(vec2f(c, s), vec2f(-s, c));
}

// The paint at lens point p (unit disc). Everything moves with paint time, so
// the pigments never stop drifting; Rumi's state sets how fast and how hard.
fn paint(p: vec2f) -> vec3f {
  let t = u.motion.x;
  let r = length(p);
  // speaking pushes rings of paint outward, listening draws them in
  let wave = 1.0 + u.motion.w * 0.22 * sin(r * 7.0 - u.motion.z);
  var q = rot(u.motion.y) * p * wave * u.look.x;
  // two passes of domain warping smear the pigment into stirred blobs
  let w1 = vec2f(fbm(q + vec2f(t * 0.07, -t * 0.05)), fbm(q + vec2f(5.2, 1.3) - t * 0.06));
  q += u.look.y * (w1 - 0.5) * 2.0;
  let w2 = vec2f(fbm(q * 1.3 + vec2f(1.7, 9.2) + t * 0.04), fbm(q * 1.3 + vec2f(8.3, 2.8) - t * 0.05));
  q += u.look.y * 0.6 * (w2 - 0.5) * 2.0;
  // each pigment claims the paint where its own field is strongest (soft-max)
  let s = u.look.z;
  let eg = exp((fbm(q) + u.green.a) * s);
  let er = exp((fbm(q + vec2f(3.1, 7.4)) + u.red.a) * s);
  let ei = exp((fbm(q + vec2f(8.3, 2.8)) + u.indigo.a) * s);
  return (eg * toLinear(u.green.rgb) + er * toLinear(u.red.rgb) + ei * toLinear(u.indigo.rgb)) / (eg + er + ei);
}

// Frosted glass: the paint blurred over a small disc of taps.
fn frosted(p: vec2f, radius: f32) -> vec3f {
  var c = paint(p);
  for (var i = 0; i < 6; i = i + 1) {
    let a = f32(i) * 1.0471976 + 0.4;
    c += paint(p + vec2f(cos(a), sin(a)) * radius);
  }
  return c / 7.0;
}

@fragment
fn fs_main(@location(0) uv: vec2f) -> @location(0) vec4f {
  let px = 2.0 * VIEW / u.frame.y;
  let p = vec2f(uv.x * 2.0 - 1.0, 1.0 - uv.y * 2.0) * VIEW;
  let r = length(p);
  let alpha = 1.0 - smoothstep(1.0 - px, 1.0 + px, r);
  if (alpha <= 0.0) { return vec4f(0.0); }
  let rr = min(r, 0.9999);
  let n = vec3f(p / max(r, 1e-4) * rr, sqrt(1.0 - rr * rr));

  // Liquid Glass bezel: near the rim, light bends inward (Snell) and splits by
  // wavelength; the centre sees the paint straight through.
  let bezel = max(u.glass.y, 1e-3);
  let depth = 1.0 - r;
  var col: vec3f;
  if (depth < bezel) {
    let x = 1.0 - depth / bezel;
    let thetaI = asin(clamp(x * x, 0.0, 1.0));
    let thetaT = asin(clamp(sin(thetaI) / u.glass.z, -1.0, 1.0));
    let bend = -tan(thetaT - thetaI) * 0.18;
    let inward = -p / max(r, 1e-4);
    let d = u.glass.w * 0.01;
    col = vec3f(
      frosted(p + inward * bend * (1.0 - d), u.glass.x).r,
      frosted(p + inward * bend, u.glass.x).g,
      frosted(p + inward * bend * (1.0 + d), u.glass.x).b
    );
  } else {
    col = frosted(p, u.glass.x);
  }

  // Luminescence: a candle inside her. Its centre sways, its radius breathes
  // with the flame's size, and a slow noise keeps the glow from sitting still.
  let lc = vec2f(-0.18 + u.flame.z * 0.22, -0.05 + u.flame.w * 0.16);
  let lr = max(u.shine.z * u.flame.y, 0.05);
  let shimmer = 0.8 + 0.4 * fbm(p * 2.2 + vec2f(u.frame.z * 0.23, -u.frame.z * 0.17));
  let lum = u.flame.x * exp(-dot(p - lc, p - lc) / (lr * lr)) * shimmer;
  let glow = toLinear(u.glow.rgb);
  let g = u.glow.a;
  // the paint is translucent: lit from inside it brightens in its own hue,
  // and the hottest core shades toward the flame's colour
  col = col * (0.4 + 1.5 * g * lum) + glow * lum * lum * 0.12 * g;
  col += glow * 0.04 * g * u.flame.x; // a little light escapes everywhere

  // the glass surface, low albedo: a faint fresnel rim and a soft top-left glare
  let m = u.frame.w;
  let fres = pow(1.0 - n.z, 3.0) * u.shine.y;
  let l = normalize(vec3f(-0.45, 0.55, 0.7));
  let h = normalize(l + vec3f(0.0, 0.0, 1.0));
  let glare = pow(max(dot(n, h), 0.0), mix(70.0, 8.0, m)) * mix(0.5, 0.12, m) * u.shine.x;
  col += vec3f(fres * 0.12 + glare);
  col += vec3f((hash(uv * u.frame.xy) - 0.5) * 0.03 * m);

  // muted: the pigments drain to grey and the candle dims
  let lumGrey = dot(col, vec3f(0.2126, 0.7152, 0.0722));
  col = mix(col, vec3f(lumGrey) * 0.8 + 0.06, u.look.w * 0.9);

  let outc = pow(clamp(col, vec3f(0.0), vec3f(1.0)), vec3f(1.0 / 2.2));
  return vec4f(outc * alpha, alpha);
}
