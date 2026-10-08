// Separable Gaussian blur of the ink, sampled by the glass.
// Ported from iyinchao/liquid-glass-studio@f7b28c3 fragment-bg-hblur/vblur.wgsl
// (MIT, (c) 2024 Charles Yin; see LICENSE-liquid-glass-studio).

struct BlurUniforms {
  dir: vec2f,   // one texel along the blur axis, in uv
  radius: f32,  // taps either side
  _pad: f32,
}

@group(0) @binding(0) var<uniform> u: BlurUniforms;
@group(0) @binding(1) var src: texture_2d<f32>;
@group(0) @binding(2) var samp: sampler;
@group(0) @binding(3) var<storage, read> weights: array<f32>;

@fragment
fn fs_main(@location(0) uv: vec2f) -> @location(0) vec4f {
  var color = textureSampleLevel(src, samp, uv, 0.0) * weights[0];
  let n = i32(u.radius);
  for (var i = 1; i <= n; i = i + 1) {
    let w = weights[i];
    let o = u.dir * f32(i);
    color += textureSampleLevel(src, samp, uv + o, 0.0) * w;
    color += textureSampleLevel(src, samp, uv - o, 0.0) * w;
  }
  return color;
}
