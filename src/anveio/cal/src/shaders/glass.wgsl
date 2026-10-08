// Liquid Glass over the ink water.
// The optics (bezel refraction via Snell's law, RGB dispersion, LCH fresnel and
// glare) are ported from iyinchao/liquid-glass-studio@f7b28c3, fragment-main.wgsl
// (MIT, (c) 2024 Charles Yin; see LICENSE-liquid-glass-studio). Changes here:
// shapes come from page elements (up to 6, liquid-merged with smin), work in
// top-down pixels, and the result is composited over the page with alpha
// instead of replacing a background image.

const PI: f32 = 3.14159265359;
const N_R: f32 = 0.98;
const N_G: f32 = 1.0;
const N_B: f32 = 1.02;
const MAX_SHAPES: u32 = 6u;

struct Shape {
  rect: vec4f, // centre x, centre y, half width, half height (css px)
  look: vec4f, // corner radius (css px), squircle exponent (2 = circular corners), shine (0..1), unused
}

struct Uniforms {
  res: vec2f,     // device px
  dpr: f32,
  count: f32,
  tint: vec4f,    // rgb, strength
  refr: vec4f,    // bezel width (css px), refractive index, dispersion, distance
  fres: vec4f,    // range, hardness, strength, unused
  glare: vec4f,   // range, hardness, convergence, opposite-side strength
  glare2: vec4f,  // strength, angle (rad), merge rate, blur under bezel (0/1)
  comp: vec4f,    // shadow spread (css px), shadow strength, cover, unused
  shapes: array<Shape, 6>,
}

@group(0) @binding(0) var<uniform> u: Uniforms;
@group(0) @binding(1) var u_blurredBg: texture_2d<f32>;
@group(0) @binding(2) var u_bg: texture_2d<f32>;
@group(0) @binding(3) var u_sampler: sampler;

fn safeAsin(x: f32) -> f32 { return asin(clamp(x, -1.0, 1.0)); }

fn safeNormalize(v: vec2f) -> vec2f {
  let len = length(v);
  if (len < 1e-8) { return vec2f(0.0); }
  return v / len;
}

fn superellipseCornerSDF(p_in: vec2f, r: f32, n: f32) -> f32 {
  let p = abs(p_in);
  return pow(pow(p.x, n) + pow(p.y, n), 1.0 / n) - r;
}

fn roundedRectSDF(p: vec2f, half: vec2f, cr: f32, n: f32) -> f32 {
  let d = abs(p) - half;
  if (d.x > -cr && d.y > -cr) {
    let cornerCenter = sign(p) * (half - vec2f(cr));
    return superellipseCornerSDF(p - cornerCenter, cr, n);
  }
  return min(max(d.x, d.y), 0.0) + length(max(d, vec2f(0.0)));
}

fn smin(a: f32, b: f32, k: f32) -> f32 {
  let h = clamp(0.5 + 0.5 * (b - a) / k, 0.0, 1.0);
  return mix(b, a, h) - k * h * (1.0 - h);
}

// Distance to the merged glass, in units of the canvas height (as upstream).
fn mainSDF(pixel: vec2f) -> f32 {
  let s = u.dpr / u.res.y;
  let p = pixel / u.res.y;
  var d = 1e5;
  let k = max(u.glare2.z, 1e-5);
  for (var i = 0u; i < MAX_SHAPES; i = i + 1u) {
    if (f32(i) >= u.count) { break; }
    let sh = u.shapes[i];
    let half = sh.rect.zw * s;
    let cr = min(sh.look.x * s, min(half.x, half.y));
    let di = roundedRectSDF(p - sh.rect.xy * s, half, cr, sh.look.y);
    if (i == 0u) { d = di; } else { d = smin(d, di, k); }
  }
  return d;
}

// How reflective the glass is here: the shine of the nearest shape. Interactive
// glass catches the light; static glass (Rumi's bubble) stays nearly matte.
fn shineAt(pixel: vec2f) -> f32 {
  let s = u.dpr / u.res.y;
  let p = pixel / u.res.y;
  var best = 1e5;
  var shine = 1.0;
  for (var i = 0u; i < MAX_SHAPES; i = i + 1u) {
    if (f32(i) >= u.count) { break; }
    let sh = u.shapes[i];
    let half = sh.rect.zw * s;
    let cr = min(sh.look.x * s, min(half.x, half.y));
    let di = roundedRectSDF(p - sh.rect.xy * s, half, cr, sh.look.y);
    if (di < best) { best = di; shine = sh.look.z; }
  }
  return shine;
}

fn getNormal(p: vec2f) -> vec2f {
  let grad = vec2f(
    mainSDF(p + vec2f(1.0, 0.0)) - mainSDF(p - vec2f(1.0, 0.0)),
    mainSDF(p + vec2f(0.0, 1.0)) - mainSDF(p - vec2f(0.0, 1.0))
  ) / 2.0;
  return grad * 1.414213562 * 1000.0;
}

#include lgs-color

fn vec2ToAngle(v: vec2f) -> f32 {
  var angle = atan2(v.y, v.x);
  if (angle < 0.0) { angle += 2.0 * PI; }
  return angle;
}

fn getTextureDispersion(v_uv: vec2f, mixRate: f32, offset: vec2f, factor: f32) -> vec4f {
  let oR = v_uv + offset * (1.0 - (N_R - 1.0) * factor);
  let oG = v_uv + offset * (1.0 - (N_G - 1.0) * factor);
  let oB = v_uv + offset * (1.0 - (N_B - 1.0) * factor);
  let bg = vec3f(
    textureSampleLevel(u_bg, u_sampler, oR, 0.0).r,
    textureSampleLevel(u_bg, u_sampler, oG, 0.0).g,
    textureSampleLevel(u_bg, u_sampler, oB, 0.0).b
  );
  let blur = vec3f(
    textureSampleLevel(u_blurredBg, u_sampler, oR, 0.0).r,
    textureSampleLevel(u_blurredBg, u_sampler, oG, 0.0).g,
    textureSampleLevel(u_blurredBg, u_sampler, oB, 0.0).b
  );
  return vec4f(mix(bg, blur, mixRate), 1.0);
}

@fragment
fn fs_main(@builtin(position) frag: vec4f, @location(0) v_uv: vec2f) -> @location(0) vec4f {
  let res1x = u.res / u.dpr;
  let pixel = frag.xy;
  let merged = mainSDF(pixel);
  let distPx = merged * u.res.y; // device px, negative inside

  // soft drop shadow outside the glass
  let shadowAlpha = u.comp.y * (1.0 - smoothstep(0.0, max(u.comp.x * u.dpr, 1.0), distPx));
  let inside = 1.0 - smoothstep(-0.75, 0.75, distPx);
  if (inside <= 0.0) {
    return vec4f(0.0, 0.0, 0.0, shadowAlpha);
  }

  let tint = u.tint.rgb;
  let tintA = u.tint.a;
  let nmerged = -1.0 * (merged * res1x.y);

  let x_R_ratio = 1.0 - nmerged / u.refr.x;
  let thetaI = safeAsin(pow(x_R_ratio, 2.0));
  let thetaT = safeAsin(1.0 / u.refr.y * sin(thetaI));
  var edgeFactor = -1.0 * tan(thetaT - thetaI);
  if (nmerged >= u.refr.x) { edgeFactor = 0.0; }

  var outColor: vec4f;
  if (edgeFactor <= 0.0) {
    outColor = textureSampleLevel(u_blurredBg, u_sampler, v_uv, 0.0);
    outColor = mix(outColor, vec4f(tint, 1.0), tintA * 0.8);
  } else {
    let edgeH = nmerged / u.refr.x;
    let normal = getNormal(pixel);
    let shine = shineAt(pixel);
    var blurMixRate = edgeH;
    if (u.glare2.w > 0.5) { blurMixRate = 1.0; }

    let refOffset = -normal * edgeFactor * shine * u.refr.w * u.dpr * vec2f(u.res.y / u.res.x, 1.0);
    let blurredPixel = getTextureDispersion(v_uv, blurMixRate, refOffset, u.refr.z * shine);

    outColor = mix(blurredPixel, vec4f(tint, 1.0), tintA * 0.8);

    // fresnel
    let fresnelFactor = clamp(
      pow(1.0 + merged * res1x.y / 1500.0 * pow(500.0 / u.fres.x, 2.0) + u.fres.y, 5.0),
      0.0, 1.0
    );
    var fresnelTintLCH = SRGB_TO_LCH(mix(vec3f(1.0), tint, tintA * 0.5));
    fresnelTintLCH.x = clamp(fresnelTintLCH.x + 20.0 * fresnelFactor * u.fres.z, 0.0, 100.0);
    outColor = mix(outColor, vec4f(LCH_TO_SRGB(fresnelTintLCH), 1.0), fresnelFactor * u.fres.z * shine * 0.7 * length(normal));

    // glare (angle measured y-up, as upstream)
    let glareGeoFactor = clamp(
      pow(1.0 + merged * res1x.y / 1500.0 * pow(500.0 / u.glare.x, 2.0) + u.glare.y, 5.0),
      0.0, 1.0
    );
    let glareAngle = (vec2ToAngle(safeNormalize(vec2f(normal.x, -normal.y))) - PI / 4.0 + u.glare2.y) * 2.0;
    var farside = false;
    if ((glareAngle > PI * 1.5 && glareAngle < PI * 3.5) || glareAngle < PI * -0.5) { farside = true; }
    var sideFactor = 1.2;
    if (farside) { sideFactor = 1.2 * u.glare.w; }
    var glareAngleFactor = (0.5 + sin(glareAngle) * 0.5) * sideFactor * u.glare2.x * shine;
    glareAngleFactor = clamp(pow(glareAngleFactor, 0.1 + u.glare.z * 2.0), 0.0, 1.0);

    var glareTintLCH = SRGB_TO_LCH(mix(blurredPixel.rgb, tint, tintA * 0.5));
    glareTintLCH.x = clamp(glareTintLCH.x + 150.0 * glareAngleFactor * glareGeoFactor, 0.0, 120.0);
    glareTintLCH.y += 30.0 * glareAngleFactor * glareGeoFactor;
    outColor = mix(outColor, vec4f(LCH_TO_SRGB(glareTintLCH), 1.0), glareAngleFactor * glareGeoFactor * length(normal));
  }

  // Composite over the page: the glass body hides `cover` of what's beneath;
  // the shadow keeps showing through the antialiased rim.
  let a = mix(shadowAlpha, u.comp.z, inside);
  let rgb = clamp(outColor.rgb, vec3f(0.0), vec3f(1.0)) * u.comp.z * inside;
  return vec4f(rgb, a);
}
