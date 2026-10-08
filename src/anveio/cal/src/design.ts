export const DESIGN = {
  orb: {
    // A microscope's magnified view of chlorophyll and a teal-blue dye
    // drifting in water deep inside her, lit from behind (orb.wgsl). Each
    // liquid's colour is what a unit thickness of it shows over the light;
    // thicker areas absorb more and deepen.
    light: "#FFF7E8", // the light behind: bright, barely warm
    chlorophyll: "#86C15C", // leaf green, as chlorophyll looks with light through it
    dye: "#5BB3AA", // teal-blue
    chlorophyllThickness: 1.15,
    dyeThickness: 1.05,
    // the steam (steam.wgsl): the wind carrying it, its direction across her
    // (degrees from horizontal) and speed (orb radii/s), pulsing in one gentle
    // rhythm (period s, depth); how quickly the air settles to the wind (1/s),
    // how strongly warm gas rises, how fast it diffuses and fades (1/s), how
    // strongly a slow swirl stirs the air, and how much gas the four drifting
    // sources feed in (thickness/s)
    driftAngle: 51,
    driftSpeed: 0.05,
    rhythmSeconds: 4.5,
    pulse: 0.35,
    steamSettle: 0.25,
    steamBuoyancy: 0.012,
    steamDiffusion: 0.15,
    steamFading: 0.05,
    steamSwirl: 0.035,
    steamFeed: 0.9,
    size: 44,
  },
  glass: {
    tint: "#FFFFFF",
    tintAlpha: 0,
    refThickness: 20,
    refFactor: 1.4,
    refDistance: 0.05,
    refDispersion: 7,
    refFresnelRange: 30,
    refFresnelHardness: 10,
    refFresnelFactor: 14,
    glareRange: 22,
    glareHardness: 10,
    glareFactor: 55,
    glareConvergence: 62,
    glareOppositeFactor: 45,
    glareAngle: -45,
    blurRadius: 8,
    blurEdge: true,
    roundness: 3,
    mergeRate: 0.005,
    shadowExpand: 25,
    shadowFactor: 15,
    cover: 0.6,
    raise: 1,
    orbLight: 1,
    domBlur: 14,
  },
  ink: {
    tint: "#1A1238",
    swell: 0.6,
    rippleSpeed: 260,
    lifetime: 4,
    gloss: 1,
    voiceAmp: 1.8,
    voiceWavelength: 180,
    voiceSpeed: 24,
    voiceReach: 320,
    voiceEase: 0.7,
  },
} as const;
