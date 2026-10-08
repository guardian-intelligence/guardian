// Jet-black ink water: a slow swell, up to 8 damped tap ripples, the gentle
// standing waves that spread from Rumi while she speaks, and her light on the
// wall behind her. The page's glass refracts this, so it picks her light up too.
struct Params {
  frame: vec4f,
  look: vec4f,
  tint: vec4f,
  voice: vec4f,
  voice2: vec4f,
  sun: vec4f,
  r0: vec4f, r1: vec4f, r2: vec4f, r3: vec4f,
  r4: vec4f, r5: vec4f, r6: vec4f, r7: vec4f,
}
@group(0) @binding(0) var<uniform> u: Params;
// Rumi's light toward the far wall, one texel per direction around her (y up,
// 0 = right), sRGB; raymarched each frame by orb.wgsl's fs_emission
@group(0) @binding(1) var u_emission: texture_2d<f32>;
@group(0) @binding(2) var u_ring: sampler;

// frame: x,y canvas pixels, z time (s), w device pixel ratio
// look: x ripple speed (css px/s), y ambient swell, z gloss, w ripple lifetime (s)
// voice: x,y Rumi's centre (css px), z strength 0..1 (eased in/out), w phase (rad)
// voice2: x wavelength (css px), y amplitude, z reach (css px), w unused
// sun: x brightness of Rumi's light, yz unused, w the far wall's distance behind her (css px); centred on voice.xy
// rN: x,y centre (css px), z start time (s), w amplitude

// Rumi's radius (css px), the dark gap kept at her rim, how bright her light
// on the far wall is, and how much its colour leans toward her nearer face.
// Glass.tsx's lightGlass uses the same falloff for the page's glass.
const ORB_RADIUS: f32 = 22.0;
const RIM_GAP: f32 = 2.0;
const WALL_BRIGHTNESS: f32 = 0.005;
const FACE_TINT: f32 = 0.3;

fn ripple(p: vec2f, r: vec4f) -> f32 {
  let age = u.frame.z - r.z;
  if (age < 0.0 || age > u.look.w || r.w <= 0.0) { return 0.0; }
  let d = distance(p, r.xy);
  let x = d - age * u.look.x;
  let width = 10.0 + age * 16.0;
  let env = exp(-(x * x) / (2.0 * width * width));
  let fade = exp(-age * 1.1) * (1.0 - age / u.look.w);
  return sin(x * 0.16) * env * fade * r.w / (1.0 + d * 0.004);
}

fn voiceWaves(p: vec2f) -> f32 {
  if (u.voice.z <= 0.0) { return 0.0; }
  let d = distance(p, u.voice.xy);
  let env = exp(-d / max(u.voice2.z, 1.0));
  return sin(d * 6.2831853 / max(u.voice2.x, 1.0) - u.voice.w) * env * u.voice2.y * u.voice.z;
}

fn height(p: vec2f) -> f32 {
  let t = u.frame.z;
  var h = u.look.y * (sin(p.x * 0.011 + t * 0.55) * sin(p.y * 0.008 - t * 0.37)
          + 0.5 * sin((p.x + p.y) * 0.019 - t * 0.81));
  h += ripple(p, u.r0) + ripple(p, u.r1) + ripple(p, u.r2) + ripple(p, u.r3);
  h += voiceWaves(p);
  h += ripple(p, u.r4) + ripple(p, u.r5) + ripple(p, u.r6) + ripple(p, u.r7);
  return h;
}

@fragment
fn fs_main(@location(0) uv: vec2f) -> @location(0) vec4f {
  let css = u.frame.xy / u.frame.w;
  let p = uv * css;
  let e = 1.25;
  let h = height(p);
  let hx = height(p + vec2f(e, 0.0)) - h;
  let hy = height(p + vec2f(0.0, e)) - h;
  let n = normalize(vec3f(-hx / e * 1.6, hy / e * 1.6, 1.0));

  let v = vec3f(0.0, 0.0, 1.0);
  let l = normalize(vec3f(-0.35, 0.75, 1.0));
  let hv = normalize(l + v);
  let spec = pow(max(dot(n, hv), 0.0), 140.0) * 0.9 * u.look.z;
  let slope = 1.0 - n.z;
  let toward = max(dot(normalize(n.xy + vec2f(1e-5)), normalize(l.xy)), 0.0);
  let glint = smoothstep(0.0, 0.02, slope) * toward * slope * 3.0 * u.look.z;
  let fres = pow(slope, 1.4);

  // a jet-black pool; only the slopes of the swell and ripples catch light
  let sheen = 0.0004 * smoothstep(0.7, 0.0, uv.y);
  var col = vec3f(sheen);
  col += vec3f(spec + glint);
  col += u.tint.rgb * fres * 1.2;
  // Rumi lights a wall far behind her (u.sun.w css px away), and we see that
  // wall past her: a small light on a distant wall spreads into a broad, soft
  // pool, its brightness falling as (1 + (d/D)^2)^-1.5 (inverse square times
  // the slant), with no detail near her. Every point of the wall sees nearly
  // all of her, so its colour is her average, tinted a little toward the face
  // of her turned that way.
  if (u.sun.w > 0.0) {
    let off = p - u.voice.xy;
    let d = length(off);
    // a thin dark ring right at her rim keeps her edge crisp against her glow
    let wall = pow(1.0 + (d * d) / (u.sun.w * u.sun.w), -1.5)
      * smoothstep(ORB_RADIUS, ORB_RADIUS + RIM_GAP, d);
    let turn = fract(atan2(-off.y, off.x) / 6.2831853 + 1.0);
    var mean = vec3f(0.0);
    for (var i = 0; i < 8; i = i + 1) {
      mean += pow(textureSampleLevel(u_emission, u_ring, vec2f(f32(i) / 8.0, 0.5), 0.0).rgb, vec3f(2.2));
    }
    let toward = pow(textureSampleLevel(u_emission, u_ring, vec2f(turn, 0.5), 0.0).rgb, vec3f(2.2));
    let face = mix(mean / 8.0, toward, FACE_TINT);
    col += face * u.sun.x * WALL_BRIGHTNESS * wall;
  }
  let vig = 1.0 - 0.35 * pow(length(uv - vec2f(0.5, 0.45)) * 1.3, 2.0);
  col *= vig;
  // black floor: the faint everywhere-sheen of the swell would read as grey after gamma
  col = max(col - vec3f(0.002), vec3f(0.0));
  let outc = pow(clamp(col, vec3f(0.0), vec3f(1.0)), vec3f(1.0 / 2.2));
  return vec4f(outc, 1.0);
}
