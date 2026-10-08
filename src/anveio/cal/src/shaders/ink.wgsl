// Jet-black ink water: a slow swell plus up to 8 damped tap ripples.
struct Params {
  frame: vec4f,
  look: vec4f,
  tint: vec4f,
  r0: vec4f, r1: vec4f, r2: vec4f, r3: vec4f,
  r4: vec4f, r5: vec4f, r6: vec4f, r7: vec4f,
}
@group(0) @binding(0) var<uniform> u: Params;

// frame: x,y canvas pixels, z time (s), w device pixel ratio
// look: x ripple speed (css px/s), y ambient swell, z gloss, w ripple lifetime (s)
// rN: x,y centre (css px), z start time (s), w amplitude

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

fn height(p: vec2f) -> f32 {
  let t = u.frame.z;
  var h = u.look.y * (sin(p.x * 0.011 + t * 0.55) * sin(p.y * 0.008 - t * 0.37)
          + 0.5 * sin((p.x + p.y) * 0.019 - t * 0.81));
  h += ripple(p, u.r0) + ripple(p, u.r1) + ripple(p, u.r2) + ripple(p, u.r3);
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
  let vig = 1.0 - 0.35 * pow(length(uv - vec2f(0.5, 0.45)) * 1.3, 2.0);
  col *= vig;
  // black floor: the faint everywhere-sheen of the swell would read as grey after gamma
  col = max(col - vec3f(0.002), vec3f(0.0));
  let outc = pow(clamp(col, vec3f(0.0), vec3f(1.0)), vec3f(1.0 / 2.2));
  return vec4f(outc, 1.0);
}
