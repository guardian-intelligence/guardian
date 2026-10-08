// Rumi: pastel oil droplets suspended in water, lit from behind, seen down a
// microscope that has drifted out of focus. Colour pools melt into one
// another; faint glowing rims are the only hint that the shapes are droplets.
//
//   light     comes from behind: a cream field, brightest at her centre
//   droplets  soft-edged, slowly drifting; each filters the light (multiply),
//             so overlaps deepen and warm and the gaps stay the brightest cream
//   rims      each droplet is drawn by a thin, soft, slightly brighter rim with
//             a faint warm fringe outside and a cool one inside
//   focus     nothing is ever sharp: focus (0..1) sets how soft fills and rims
//             are; it breathes at rest and pulls in while she listens
//   eyepiece  a perfect circle whose edge feathers into the dark
//
// fs_emission averages what each side of her shows into one texel per
// direction: the light she throws on the far wall and the page's glass.

struct Params {
  frame: vec4f,  // canvas px x, y, wall-clock time (s), focus 0..1
  motion: vec4f, // drift time (s, state-paced), gather 0..1, brightness pulse, mute 0..1
  cream: vec4f,  // rgb, unused
  butter: vec4f, // rgb, unused
  sage: vec4f,   // rgb, unused
  teal: vec4f,   // rgb, unused
  rim: vec4f,    // rgb, opacity
  look: vec4f,   // fringe strength, unused x3
}
@group(0) @binding(0) var<uniform> u: Params;

// canvas half-width in lens units; matches BLEED in Orb.tsx
const VIEW: f32 = 1.12;
const PI: f32 = 3.14159265;
const DROPLETS: i32 = 7;
// droplets fill most of the field, leaving thin cream gaps
const DROPLET_SCALE: f32 = 1.18;
// seconds for the arrangement to turn once at rest, and how far each droplet wanders (lens units)
const REARRANGE: f32 = 30.0;
const WANDER: f32 = 0.3;
// how much the water's flow bends the droplets (lens units), how far it drags
// them along the current, and how long the colour streaks it leaves are
const FLOW_WARP: f32 = 0.16;
const FLOW_DRAG: f32 = 0.05;
const STREAK: f32 = 0.025;
// the eyepiece edge feathers over the outer eighth of her radius
const FEATHER: f32 = 0.12;

fn toLinear(c: vec3f) -> vec3f { return pow(c, vec3f(2.2)); }

fn hash2(p: vec2f) -> f32 {
  return fract(sin(dot(p, vec2f(127.1, 311.7))) * 43758.5453);
}

fn noise(p: vec2f) -> f32 {
  let i = floor(p);
  let f = fract(p);
  let w = f * f * (3.0 - 2.0 * f);
  return mix(mix(hash2(i), hash2(i + vec2f(1.0, 0.0)), w.x),
             mix(hash2(i + vec2f(0.0, 1.0)), hash2(i + vec2f(1.0, 1.0)), w.x), w.y);
}

// Droplet i's home (x, y) and radius. One large teal mass sits low and to the
// left so she never looks evenly busy; the rest are mixed sizes.
fn home(i: i32) -> vec3f {
  switch i {
    case 0: { return vec3f(-0.4, -0.44, 0.47); }
    case 1: { return vec3f(0.05, 0.42, 0.36); }
    case 2: { return vec3f(0.48, 0.12, 0.38); }
    case 3: { return vec3f(-0.42, 0.22, 0.32); }
    case 4: { return vec3f(0.3, -0.5, 0.3); }
    case 5: { return vec3f(0.02, -0.02, 0.3); }
    default: { return vec3f(0.62, -0.18, 0.2); }
  }
}

// How droplet i filters the cream light. Green leads; teal sits among the
// greens so the blue bleeds into them; butter warms the rest.
fn tint(i: i32) -> vec3f {
  let cream = toLinear(u.cream.rgb);
  switch i {
    case 0, 4: { return toLinear(u.teal.rgb) / cream; }
    case 2, 3, 5: { return toLinear(u.sage.rgb) / cream; }
    default: { return toLinear(u.butter.rgb) / cream; }
  }
}

// The droplets at point q: their blended colour (how they filter the light),
// and how far inside the closest two are (d1 <= d2; negative is inside).
struct Foam {
  body: vec3f,
  d1: f32,
  d2: f32,
}

fn foam(q: vec2f, tau: f32) -> Foam {
  let t = u.motion.x;
  var ds: array<f32, 7>;
  var d1 = 1e3;
  var d2 = 1e3;
  for (var i = 0; i < DROPLETS; i = i + 1) {
    let fi = f32(i);
    let h = home(i);
    // the whole arrangement slowly turns (a full rearrangement about every
    // REARRANGE seconds) while each droplet wanders its own loop; gathering
    // draws them together so they meet and merge
    let turn = t * 2.0 * PI / REARRANGE;
    let around = mat2x2f(vec2f(cos(turn), sin(turn)), vec2f(-sin(turn), cos(turn))) * h.xy;
    let path = vec2f(sin(t * (0.23 + 0.05 * fi) + fi * 1.7), cos(t * (0.2 + 0.06 * fi) + fi * 2.9));
    let centre = mix(around, around * 0.55, u.motion.y) + path * WANDER;
    let radius = h.z * DROPLET_SCALE * (1.0 + 0.12 * u.motion.y);
    // a touch oval, turning slowly
    let spin = t * 0.035 + fi;
    let rel = mat2x2f(vec2f(cos(spin), sin(spin)), vec2f(-sin(spin), cos(spin))) * (q - centre);
    let d = length(rel * vec2f(0.93, 1.07)) - radius;
    ds[i] = d;
    if (d < d1) { d2 = d1; d1 = d; } else if (d < d2) { d2 = d; }
  }
  // colours melt across shared walls: a soft blend of the droplets the point
  // sits most inside, so nothing creases where a third droplet takes over
  var body = vec3f(0.0);
  var wsum = 0.0;
  for (var i = 0; i < DROPLETS; i = i + 1) {
    let w = exp(-(ds[i] - d1) / tau);
    body += tint(i) * w;
    wsum += w;
  }
  return Foam(body / wsum, d1, d2);
}

// The water's flow at p: the curl of a slowly changing noise potential, so it
// swirls without sources or sinks, like a real incompressible fluid.
fn flow(p: vec2f, t: f32) -> vec2f {
  let e = 0.05;
  let s = p * 1.3 + vec2f(t * 0.04, -t * 0.03);
  let dx = noise(s + vec2f(e, 0.0)) - noise(s - vec2f(e, 0.0));
  let dy = noise(s + vec2f(0.0, e)) - noise(s - vec2f(0.0, e));
  return vec2f(dy, -dx) / (2.0 * e);
}

// What she shows at lens point p (unit disc), in linear light.
//
// Oil in water, up close: the flow drags every droplet into wavy, stretched
// shapes and smears its colour into silky streaks along the current. Each
// droplet's edge is a meniscus lens, not a painted line: a bright core with a
// dark shadow just outside it, thickening and thinning along its length, with
// fainter echo lines where thin films ripple beside it, colour fringes in
// segments, and the odd specular glint.
fn scene(p: vec2f) -> vec3f {
  let t = u.motion.x;
  let focus = u.frame.w;
  // nothing is fully sharp: fills blur over a wide band, rims over a narrow one
  let blur = mix(0.14, 0.06, focus);
  let width = mix(0.045, 0.02, focus);
  let tau = blur * 1.9;

  // advect: the flow warps the space the droplets sit in
  let v = flow(p, t);
  let warp = vec2f(noise(p * 2.1 + vec2f(t * 0.09, 3.1)), noise(p * 2.1 + vec2f(7.7, -t * 0.08))) - 0.5;
  let q = p + warp * FLOW_WARP + v * FLOW_DRAG;

  // light from behind: cream, brightest at her centre
  let cream = toLinear(u.cream.rgb) * (1.1 - 0.3 * dot(p, p));

  // the colour, smeared along the current (a short line integral along it)
  let f = foam(q, tau);
  // (scaled by the current's speed, so still water leaves no streak)
  let along = v * STREAK;
  var body = f.body * 2.0;
  body += foam(q + along, tau).body + foam(q - along, tau).body;
  body += 0.5 * (foam(q + along * 2.0, tau).body + foam(q - along * 2.0, tau).body);
  body /= 5.0;

  // fills filter the light behind, clearest at a droplet's middle and deepest
  // toward its rim; where two press together the wall deepens (multiply)
  let d1 = f.d1;
  let d2 = f.d2;
  let own = (1.0 - smoothstep(-blur, blur, d1)) * mix(1.0, 0.82, smoothstep(0.0, -0.35, d1));
  let pressed = (1.0 - smoothstep(-blur, blur, d2)) * (1.0 - smoothstep(0.0, blur, d2 - d1));
  // deepened a little, so the pools read as colour and not tinted cream
  let deep = pow(body, vec3f(1.15));
  var col = cream * mix(vec3f(1.0), deep, own) * mix(vec3f(1.0), deep, 0.25 * pressed);

  // the meniscus: distance to the nearest wall (a shared wall where both
  // droplets hold the point, else the open edge), its thickness wandering
  // signed: negative inside a droplet, positive in the open water
  let sd = select(d1, -min(-d1, 0.5 * (d2 - d1) + max(d2, 0.0) * 4.0), d1 < 0.0);
  let w = width * (0.45 + 1.1 * noise(q * 3.3 + vec2f(-t * 0.06, t * 0.05)));
  let core = exp(-((sd + w * 0.4) * (sd + w * 0.4)) / (w * w));
  let shadow = exp(-((sd - w * 1.1) * (sd - w * 1.1)) / (w * w * 1.6));
  // only some edges keep a visible meniscus; the rest have melted away
  let kept = smoothstep(0.3, 0.7, noise(q * 1.3 + vec2f(t * 0.02, 9.0)));
  let rim = toLinear(u.rim.rgb) * u.rim.a * (0.25 + 0.75 * kept);
  col += rim * core * 0.55;
  col *= 1.0 - 0.22 * shadow * (0.25 + 0.75 * kept);
  // echo lines where thin films ripple beside an edge, in some places only
  let films = smoothstep(0.45, 0.75, noise(q * 1.7 + vec2f(t * 0.03, 5.0)));
  for (var k = 1; k < 3; k = k + 1) {
    let o = f32(k) * 0.075 * (1.0 + 0.3 * sin(dot(q, vec2f(3.0, 2.0)) + t * 0.2));
    col += rim * exp(-((sd - o) * (sd - o)) / (w * w * 0.6)) * films * 0.22 / f32(k);
  }
  // where droplets are about to merge, the wall between them pinches brighter
  col += rim * core * 0.3 * u.motion.y * pressed;
  // colour fringes, warm outside and cool inside, only along some stretches
  let fringe = smoothstep(0.4, 0.8, noise(q * 2.7 - vec2f(t * 0.07, 1.3))) * u.look.x;
  col += vec3f(1.0, 0.55, 0.15) * exp(-((sd - w) * (sd - w)) / (w * w * 0.5)) * fringe * 0.35;
  col += vec3f(0.15, 0.55, 1.0) * exp(-((sd + w * 1.2) * (sd + w * 1.2)) / (w * w * 0.5)) * fringe * 0.35;
  // the odd specular glint riding a rim
  col += vec3f(1.0) * core * kept * pow(noise(q * 8.0 + vec2f(t * 0.1, 0.0)), 22.0) * 0.8;
  return col;
}

fn quiet(c: vec3f) -> vec3f {
  // speaking brightens her a little in time with her voice; muted she keeps
  // her colours but quiets, a little dimmer and softer
  let lit = c * (1.0 + 0.08 * u.motion.z);
  let grey = dot(lit, vec3f(0.2126, 0.7152, 0.0722));
  return mix(lit, vec3f(grey), u.motion.w * 0.3) * (1.0 - 0.25 * u.motion.w);
}

@fragment
fn fs_main(@location(0) uv: vec2f) -> @location(0) vec4f {
  let p = vec2f(uv.x * 2.0 - 1.0, 1.0 - uv.y * 2.0) * VIEW;
  let r = length(p);
  // the eyepiece: a perfect circle that feathers into the dark
  let alpha = 1.0 - smoothstep(1.0 - FEATHER, 1.0, r);
  if (alpha <= 0.0) { return vec4f(0.0); }
  var col = quiet(scene(p));
  col *= mix(1.0, 0.6, smoothstep(0.65, 1.0, r));
  // a whisper of dither so the soft gradients never band
  col += vec3f((hash2(uv * u.frame.xy + u.frame.z) - 0.5) / 255.0);
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
  light = quiet(light / 7.0);
  return vec4f(pow(clamp(light, vec3f(0.0), vec3f(1.0)), vec3f(1.0 / 2.2)), 1.0);
}
