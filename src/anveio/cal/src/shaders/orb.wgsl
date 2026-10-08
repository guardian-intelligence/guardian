// Rumi: a glass sphere of flowing colour; waves roll out while she speaks.
struct Params {
  frame: vec4f,
  wave: vec4f,
  c1: vec4f,
  c2: vec4f,
  c3: vec4f,
  look: vec4f,
}
@group(0) @binding(0) var<uniform> u: Params;

const TAU: f32 = 6.2831853;
const VIEW: f32 = 1.12;

fn toLinear(c: vec3f) -> vec3f { return pow(c, vec3f(2.2)); }

fn hash(p: vec2f) -> f32 {
  return fract(sin(dot(p, vec2f(127.1, 311.7))) * 43758.5453);
}

fn noise(p: vec2f) -> f32 {
  let i = floor(p);
  let f = fract(p);
  let w = f * f * (3.0 - 2.0 * f);
  return mix(mix(hash(i), hash(i + vec2f(1.0, 0.0)), w.x),
             mix(hash(i + vec2f(0.0, 1.0)), hash(i + vec2f(1.0, 1.0)), w.x), w.y);
}

fn rot(a: f32) -> mat2x2f {
  let c = cos(a); let s = sin(a);
  return mat2x2f(vec2f(c, s), vec2f(-s, c));
}

// wave: x energy, y ripple phase, z swirl angle, w muted 0..1
// look: x gloss, y ripple density, z drift time, w how far muting drains the colour
@fragment
fn fs_main(@location(0) uv: vec2f) -> @location(0) vec4f {
  let px = 2.0 * VIEW / u.frame.y;
  let p = vec2f(uv.x * 2.0 - 1.0, 1.0 - uv.y * 2.0) * VIEW;
  let r = length(p);
  let alpha = 1.0 - smoothstep(1.0 - px, 1.0 + px, r);
  if (alpha <= 0.0) { return vec4f(0.0); }

  let rr = min(r, 0.9999);
  let n = vec3f(p / max(r, 1e-4) * rr, sqrt(1.0 - rr * rr));
  let c1 = toLinear(u.c1.rgb);
  let c2 = toLinear(u.c2.rgb);
  let c3 = toLinear(u.c3.rgb);
  let t = u.look.z;
  let energy = u.wave.x;

  // flowing colour field, seen through the sphere so it bends at the edge
  var q = rot(u.wave.z) * (n.xy / (0.6 + 0.4 * n.z)) * 1.4;
  q += 0.35 * vec2f(sin(q.y * 1.9 + t * 0.55), cos(q.x * 1.6 - t * 0.47));
  let f1 = noise(q * 1.3 + vec2f(t * 0.12, -t * 0.09));
  let f2 = noise(q * 2.1 - vec2f(t * 0.07, t * 0.11) + 4.0);
  var col = mix(c2, c1, smoothstep(0.35, 0.95, f1) * 0.7);
  col = mix(col, c3, smoothstep(0.6, 0.95, f2) * 0.4);

  // waves of colour rolling out from the centre while she speaks
  let warped = length(q) + 0.18 * (f1 - 0.5);
  let ripple = 0.5 + 0.5 * sin(warped * u.look.y - u.wave.y);
  let band = smoothstep(0.3, 1.0, ripple) * energy;
  col = mix(col, mix(c1, c3, 0.35 + 0.4 * ripple), band * 0.5);
  col += c3 * band * 0.12;

  // glass: soft light from the top left, bright rim
  let l = normalize(vec3f(-0.45, 0.55, 0.7));
  let h = normalize(l + vec3f(0.0, 0.0, 1.0));
  let fres = pow(1.0 - n.z, 2.5);
  let diffuse = 0.78 + 0.22 * dot(n, l);
  let spec = pow(max(dot(n, h), 0.0), 60.0) * 0.55 * u.look.x;
  let sheen = pow(max(dot(n, normalize(vec3f(-0.3, 0.75, 0.6))), 0.0), 9.0) * 0.14 * u.look.x;
  col = col * diffuse + vec3f(spec + sheen) + mix(c2, vec3f(1.0), 0.5) * fres * 0.35;

  // muted: the colour drains to a cool grey
  let lum = dot(col, vec3f(0.2126, 0.7152, 0.0722));
  col = mix(col, (vec3f(lum) * 0.55 + 0.42) * vec3f(0.97, 0.96, 1.0), u.wave.w * u.look.w);

  let outc = pow(clamp(col, vec3f(0.0), vec3f(1.0)), vec3f(1.0 / 2.2));
  return vec4f(outc * alpha, alpha);
}
