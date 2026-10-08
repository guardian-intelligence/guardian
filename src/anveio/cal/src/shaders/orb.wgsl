// Rumi: a microscope's circular view of two coloured gases, green and teal,
// drifting and diffusing like steam inside her, lit from behind.
//
//   gas      simulated as a real fluid in steam.wgsl (Orb.tsx steps it each
//            frame); here it is only seen. Each gas absorbs the light passing
//            through it by how thick it is (Beer-Lambert): thick plumes
//            deepen into saturated colour, thin wisps stay luminous
//   focus    the microscope is focused at one depth; the green (deeper) and
//            the teal blur by their distance from it. Focus breathes and
//            shifts with her state
//   eyepiece a perfect circle whose edge feathers into the dark
//
// fs_emission averages what each side of her shows into one texel per
// direction: the light she throws on the far wall and the page's glass.

struct Params {
  frame: vec4f,       // canvas px x, y, wall-clock time (s), focal depth (-1 deep .. 1 near)
  flow: vec4f,        // unused, voice pulse, mute 0..1, unused
  light: vec4f,       // rgb (the light behind), unused
  chlorophyll: vec4f, // rgb (the green gas's colour at unit thickness), thickness scale
  dye: vec4f,         // rgb (the teal gas's colour at unit thickness), thickness scale
}
@group(0) @binding(0) var<uniform> u: Params;
// the steam (steam.ts): r green thickness, g teal thickness, wrapping
@group(0) @binding(1) var gas: texture_2d<f32>;
@group(0) @binding(2) var wrap: sampler;

// canvas half-width in lens units; matches BLEED in Orb.tsx
const VIEW: f32 = 1.12;
const PI: f32 = 3.14159265;
// how much of the steam's wrap-around grid one lens unit spans (the view
// shows the middle 2 * VIEW * SPAN of it)
const SPAN: f32 = 0.36;
// the eyepiece edge feathers over the outer eighth of her radius
const FEATHER: f32 = 0.12;
// depths of the two gases (-1 deep .. 1 near)
const GREEN_DEPTH: f32 = -0.95;
const TEAL_DEPTH: f32 = -0.45;
// blur (lens units) in perfect focus, and added per unit of depth from focus
const SHARP: f32 = 0.012;
const DEFOCUS: f32 = 0.09;
// the backlight's brightness at her centre, and where highlights start to roll off (linear)
const EXPOSURE: f32 = 0.95;
const KNEE: f32 = 0.7;
// film grain strength (fraction of brightness, peak to peak)
const GRAIN: f32 = 0.09;

fn toLinear(c: vec3f) -> vec3f { return pow(c, vec3f(2.2)); }

fn hash2(p: vec2f) -> f32 {
  return fract(sin(dot(p, vec2f(127.1, 311.7))) * 43758.5453);
}

// How blurred a layer at depth z is, given where the microscope is focused.
fn blurAt(z: f32) -> f32 {
  return SHARP + DEFOCUS * abs(z - u.frame.w);
}

// The gases seen at lens point p, blurred by the microscope's focus: the
// green lies deeper than the teal, so each takes its own blur.
fn gases(p: vec2f) -> vec2f {
  let uv = 0.5 + vec2f(p.x, -p.y) * SPAN;
  var green = 0.0;
  var teal = 0.0;
  let bg = blurAt(GREEN_DEPTH) * SPAN;
  let bt = blurAt(TEAL_DEPTH) * SPAN;
  for (var i = 0; i < 5; i = i + 1) {
    let a = f32(i) * 1.2566371;
    let o = select(vec2f(cos(a), sin(a)), vec2f(0.0), i == 0);
    green += textureSampleLevel(gas, wrap, uv + o * bg, 0.0).r;
    teal += textureSampleLevel(gas, wrap, uv + o * bt, 0.0).g;
  }
  return vec2f(green, teal) / 5.0;
}

// How much a liquid of this colour (at unit thickness) absorbs per unit of
// thickness, by channel.
fn absorbance(c: vec3f) -> vec3f {
  return -log(max(toLinear(c), vec3f(1e-3)));
}

// What she shows at lens point p (unit disc), in linear light.
fn scene(p: vec2f) -> vec3f {
  // light from behind, with headroom so it never clips, then absorbed by the
  // two gases in proportion to how thick each is
  let backlight = toLinear(u.light.rgb) * (EXPOSURE - 0.2 * dot(p, p));
  let g = gases(p);
  let green = g.x * u.chlorophyll.a;
  let teal = g.y * u.dye.a;
  let absorbed = absorbance(u.chlorophyll.rgb) * green + absorbance(u.dye.rgb) * teal;
  return backlight * exp(-absorbed);
}

// Highlights roll off softly instead of clipping to white, keeping their hue.
fn shoulder(c: vec3f) -> vec3f {
  let lum = dot(c, vec3f(0.2126, 0.7152, 0.0722));
  if (lum <= KNEE) { return c; }
  let rolled = KNEE + (1.0 - KNEE) * (1.0 - exp(-(lum - KNEE) / (1.0 - KNEE)));
  return c * (rolled / lum);
}

fn quiet(c: vec3f) -> vec3f {
  // speaking brightens her a little in time with her voice; muted she keeps
  // her colours but quiets, a little dimmer
  let lit = c * (1.0 + 0.08 * u.flow.y);
  let grey = dot(lit, vec3f(0.2126, 0.7152, 0.0722));
  return mix(lit, vec3f(grey), u.flow.z * 0.08) * (1.0 - 0.18 * u.flow.z);
}

@fragment
fn fs_main(@location(0) uv: vec2f) -> @location(0) vec4f {
  let p = vec2f(uv.x * 2.0 - 1.0, 1.0 - uv.y * 2.0) * VIEW;
  let r = length(p);
  // the eyepiece: a perfect circle that feathers into the dark
  let alpha = 1.0 - smoothstep(1.0 - FEATHER, 1.0, r);
  if (alpha <= 0.0) { return vec4f(0.0); }
  var col = shoulder(quiet(scene(p)));
  col *= mix(1.0, 0.6, smoothstep(0.65, 1.0, r));
  // film grain over everything, re-cast 24 times a second like real film
  // (it also keeps the soft gradients from banding)
  let frame24 = floor(u.frame.z * 24.0);
  col *= 1.0 + GRAIN * (hash2(floor(uv * u.frame.xy) + frame24 * 17.31) - 0.5);
  let outc = pow(clamp(col, vec3f(0.0), vec3f(1.0)), vec3f(1.0 / 2.2));
  return vec4f(outc * alpha, alpha);
}

// One texel per direction around her (y up, 0 = right): what the side of her
// turned that way shows, averaged, as the light she throws back off the wall.
@fragment
fn fs_emission(@location(0) uv: vec2f) -> @location(0) vec4f {
  let a = uv.x * 2.0 * PI;
  let dir = vec2f(cos(a), sin(a));
  let side = vec2f(-dir.y, dir.x);
  var light = scene(dir * 0.5);
  for (var i = 0; i < 6; i = i + 1) {
    let b = f32(i) * PI / 3.0;
    light += scene(dir * 0.5 + (dir * cos(b) + side * sin(b)) * 0.3);
  }
  light = shoulder(quiet(light / 7.0));
  return vec4f(pow(clamp(light, vec3f(0.0), vec3f(1.0)), vec3f(1.0 / 2.2)), 1.0);
}
