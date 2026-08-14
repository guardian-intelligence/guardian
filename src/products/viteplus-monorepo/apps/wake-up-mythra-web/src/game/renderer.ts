// The isometric view: 2:1 diamonds, 16x8 px tiles, 5 px per elevation
// level. A pure consumer of (terrain planes, the presented world's view
// buffer, camera) — it reads what the core presents and never touches the
// protocol. The camera is presentation-only; the sim has no concept of one.

import type { Core, TerrainPlanes } from "@guardian/mythrad-client-core";
import * as v from "valibot";
import type { Hud } from "./hud";
import type { Jank } from "./jank";

const Q16 = 65536;
const TILE_W = 16;
const TILE_H = 8;
const ELEV_PX = 5;
const ISO_PAD = TILE_H * 4; // top margin so raised tiles stay in-layer
/** The sim_view stride: id u64, x i32, y i32 (Q16.16 cells), flags u8, facing u8, anim u8, pad. */
const VIEW_STRIDE = 20;
/** How far a dog may drift from its presented position before the smoother snaps it. */
const SNAP_Q16 = 8 * Q16;

// Camera: follow by default, free-pan with iOS-scroll momentum. A drag
// past the slop switches to free-cam and pans 1:1; release hands the last
// ~100ms of gesture velocity to a coast that decays by UIScrollView's
// documented deceleration rate (0.998 per millisecond) — the reference
// curve for "feels native on iOS". A bounds clamp kills velocity on the
// struck axis.
const DECEL = 0.998;
const DRAG_SLOP_CSS = 8;
// Only motion within the last 100ms of a gesture speaks for the release,
// and only above this threshold: a drag that slowed or paused before
// lifting is a placement and stays planted (the same discrimination
// UIPanGestureRecognizer applies before a scroll view coasts). ~0.35
// canvas-px/ms is a deliberate flick at our scale.
const FLICK_MIN = 0.35;
const FLICK_WINDOW_MS = 100;

type DogView = {
  id: bigint;
  xq: number;
  yq: number;
  flags: number;
  anim: number;
};

export type Renderer = {
  /** Draws one frame from the rAF timestamp, which also paces the camera. */
  readonly frame: (now: number) => void;
  /** Returns the camera to the default follow behavior. */
  readonly followCamera: () => void;
};

export function createRenderer(opts: {
  readonly core: Core;
  readonly hud: Hud;
  /** Measures what was drawn, frame by frame. */
  readonly jank: Jank;
  /** The dog to highlight and follow. Follows a sign-in, so it is read per frame. */
  readonly myDog: () => bigint;
}): Renderer {
  const { core, hud, jank } = opts;
  const canvas = v.parse(v.instance(HTMLCanvasElement), document.getElementById("grid"));
  const ctx = v.parse(v.instance(CanvasRenderingContext2D), canvas.getContext("2d"));

  let terrain: TerrainPlanes | null = null;
  let groundLayer: HTMLCanvasElement | null = null;
  let deckLayer: HTMLCanvasElement | null = null;
  let isoOriginX = 0; // world-pixel x of cell (0,0)'s diamond center
  let lastCam: [number, number] = [0, 0];

  let camFree = false;
  const cam: [number, number] = [0, 0];
  const camV: [number, number] = [0, 0]; // canvas px per ms
  let camT = 0;
  let dragging = false;
  let dragMoved = false;
  let downClient: [number, number] = [0, 0];
  let dragLast: [number, number] | null = null;
  const dragSamples: { t: number; dx: number; dy: number }[] = [];

  const prevPos = new Map<string, [number, number]>();
  let quads = new Int32Array(0);

  function setCamFree(free: boolean): void {
    camFree = free;
    if (!free) {
      camV[0] = 0;
      camV[1] = 0;
    }
    hud.showRecenter(free);
  }

  function canvasPt(e: PointerEvent): [number, number] {
    const rect = canvas.getBoundingClientRect();
    return [
      ((e.clientX - rect.left) * canvas.width) / rect.width,
      ((e.clientY - rect.top) * canvas.height) / rect.height,
    ];
  }

  canvas.addEventListener("pointerdown", (e) => {
    canvas.setPointerCapture(e.pointerId);
    dragging = true;
    dragMoved = false;
    camV[0] = 0; // a touch during a coast grabs the map, iOS-style
    camV[1] = 0;
    downClient = [e.clientX, e.clientY];
    dragLast = canvasPt(e);
    dragSamples.length = 0;
  });
  canvas.addEventListener("pointermove", (e) => {
    if (!dragging || !dragLast) return;
    if (!dragMoved) {
      const dist = Math.hypot(e.clientX - downClient[0], e.clientY - downClient[1]);
      if (dist < DRAG_SLOP_CSS) return;
      dragMoved = true;
      setCamFree(true);
    }
    const [cx, cy] = canvasPt(e);
    const dx = cx - dragLast[0];
    const dy = cy - dragLast[1];
    dragLast = [cx, cy];
    cam[0] -= dx;
    cam[1] -= dy;
    const now = performance.now();
    dragSamples.push({ t: now, dx, dy });
    while (dragSamples.length && now - dragSamples[0]!.t > FLICK_WINDOW_MS) dragSamples.shift();
  });
  const endDrag = (): void => {
    if (!dragging) return;
    dragging = false;
    if (dragMoved) {
      const now = performance.now();
      const recent = dragSamples.filter((s) => now - s.t <= FLICK_WINDOW_MS);
      if (recent.length >= 2) {
        const span = now - recent[0]!.t;
        if (span > 1) {
          let sx = 0;
          let sy = 0;
          for (const s of recent) {
            sx += s.dx;
            sy += s.dy;
          }
          const vx = -sx / span;
          const vy = -sy / span;
          if (Math.hypot(vx, vy) >= FLICK_MIN) {
            camV[0] = vx;
            camV[1] = vy;
          }
        }
      }
    }
    dragSamples.length = 0;
  };
  canvas.addEventListener("pointerup", endDrag);
  canvas.addEventListener("pointercancel", endDrag);

  // A tap says "walk there": invert the iso projection at ground level and
  // send a move_to for the tapped cell. A pan is not a tap — the slop
  // discriminator above set dragMoved for this gesture.
  canvas.addEventListener("click", (e) => {
    if (dragMoved || !terrain || core.state.role !== "player") return;
    const rect = canvas.getBoundingClientRect();
    const px = ((e.clientX - rect.left) * canvas.width) / rect.width + lastCam[0];
    const py = ((e.clientY - rect.top) * canvas.height) / rect.height + lastCam[1];
    const wx = (px - isoOriginX) / (TILE_W / 2);
    const wy = (py - ISO_PAD) / (TILE_H / 2);
    const cellX = Math.floor((wx + wy) / 2);
    const cellY = Math.floor((wy - wx) / 2);
    if (cellX < 0 || cellY < 0 || cellX >= terrain.w || cellY >= terrain.h) return;
    core.moveTo(cellY * terrain.w + cellX);
  });

  function shade(hex: string, f: number): string {
    const n = parseInt(hex.slice(1), 16);
    const c = (value: number) => Math.max(0, Math.min(255, Math.round(value * f)));
    return `rgb(${c(n >> 16)},${c((n >> 8) & 255)},${c(n & 255)})`;
  }

  function diamond(g: CanvasRenderingContext2D, cx: number, cy: number, fill: string): void {
    g.fillStyle = fill;
    g.beginPath();
    g.moveTo(cx, cy - TILE_H / 2);
    g.lineTo(cx + TILE_W / 2, cy);
    g.lineTo(cx, cy + TILE_H / 2);
    g.lineTo(cx - TILE_W / 2, cy);
    g.closePath();
    g.fill();
  }

  // A tile's two viewer-facing side faces, dropped `depth` px.
  function skirt(
    g: CanvasRenderingContext2D,
    cx: number,
    cy: number,
    depth: number,
    fill: string,
  ): void {
    g.fillStyle = shade(fill, 0.62);
    g.beginPath();
    g.moveTo(cx - TILE_W / 2, cy);
    g.lineTo(cx, cy + TILE_H / 2);
    g.lineTo(cx, cy + TILE_H / 2 + depth);
    g.lineTo(cx - TILE_W / 2, cy + depth);
    g.closePath();
    g.fill();
    g.fillStyle = shade(fill, 0.5);
    g.beginPath();
    g.moveTo(cx + TILE_W / 2, cy);
    g.lineTo(cx, cy + TILE_H / 2);
    g.lineTo(cx, cy + TILE_H / 2 + depth);
    g.lineTo(cx + TILE_W / 2, cy + depth);
    g.closePath();
    g.fill();
  }

  function prerenderTerrain(planes: TerrainPlanes): void {
    const { w, h } = planes;
    isoOriginX = (h * TILE_W) / 2;
    const pw = ((w + h) * TILE_W) / 2 + TILE_W;
    const ph = ((w + h) * TILE_H) / 2 + ISO_PAD + TILE_H * 2;
    groundLayer = document.createElement("canvas");
    groundLayer.width = pw;
    groundLayer.height = ph;
    deckLayer = document.createElement("canvas");
    deckLayer.width = pw;
    deckLayer.height = ph;
    const g = v.parse(v.instance(CanvasRenderingContext2D), groundLayer.getContext("2d"));
    const d = v.parse(v.instance(CanvasRenderingContext2D), deckLayer.getContext("2d"));
    const GRASS = ["#79b356", "#7cb85c", "#76ad53", "#82bd63"];
    for (let y = 0; y < h; y++) {
      for (let x = 0; x < w; x++) {
        const i = y * w + x;
        const cls = planes.ground[i]!;
        const el = planes.elev[i]!;
        // Diamond centers sit at CELL centers in worldToPx's frame — the
        // +TILE_H/2 keeps tiles, dogs, and click targeting on one grid.
        const cx = isoOriginX + ((x - y) * TILE_W) / 2;
        const cy = ISO_PAD + ((x + y + 1) * TILE_H) / 2 - el * ELEV_PX;
        let fill: string;
        if (cls === 0) {
          fill = "#2c2f38"; // block: dark wall cap
        } else if (cls === 2) {
          fill = el > 0 ? "#63b1ec" : "#4d9de0"; // water, brighter upstream
        } else if (cls === 3) {
          fill = "#a89a84"; // stairs
        } else {
          const variant = planes.variant[i]!;
          fill = variant === 8 ? "#c9ad7c" : GRASS[variant % 4]!; // path vs grass tufts
        }
        if (el > 0 || cls === 0) {
          const depth = (cls === 0 ? 1 : el) * ELEV_PX + (cls === 0 ? ELEV_PX : 0);
          // a waterfall face is the river pouring over its own ledge
          const southLower =
            cls === 2 && y + 1 < h && planes.ground[i + w] === 2 && planes.elev[i + w]! < el;
          skirt(g, cx, cy, depth, southLower ? "#d8ecf8" : fill);
        }
        diamond(g, cx, cy, fill);
        if (cls === 3) {
          // tread lines sell the stairs
          g.strokeStyle = shade(fill, 0.75);
          g.lineWidth = 1;
          for (const t of [-2, 0, 2]) {
            g.beginPath();
            g.moveTo(cx - TILE_W / 4, cy + t);
            g.lineTo(cx + TILE_W / 4, cy + t);
            g.stroke();
          }
        }
        const mask = planes.obstacle[2 * i]! | (planes.obstacle[2 * i + 1]! << 8);
        if (mask) {
          for (let s = 0; s < 16; s++) {
            if (!(mask & (1 << s))) continue;
            const sx = ((s & 3) + 0.5) / 4;
            const sy = ((s >> 2) + 0.5) / 4;
            const ox = cx + ((sx - sy) * TILE_W) / 2;
            const oy = cy - TILE_H / 2 + ((sx + sy) * TILE_H) / 2;
            g.fillStyle = "#6b4f35";
            g.fillRect(ox - 2, oy - 3, 4, 4);
          }
        }
        const dk = planes.deck[i]!;
        if (dk) {
          const dy = ISO_PAD + ((x + y + 1) * TILE_H) / 2 - (dk - 1) * ELEV_PX;
          skirt(d, cx, dy, 2, "#a9743f");
          diamond(d, cx, dy, "#b98a52");
          d.strokeStyle = "#8a6238";
          d.lineWidth = 1;
          d.beginPath();
          d.moveTo(cx - TILE_W / 4, dy);
          d.lineTo(cx + TILE_W / 4, dy);
          d.stroke();
        }
      }
    }
  }

  // World Q16.16 cell coords -> layer pixel coords at a given standing
  // elevation (in levels).
  function worldToPx(xq: number, yq: number, elevLevels: number): [number, number] {
    const fx = xq / Q16;
    const fy = yq / Q16;
    return [
      isoOriginX + ((fx - fy) * TILE_W) / 2,
      ISO_PAD + ((fx + fy) * TILE_H) / 2 - elevLevels * ELEV_PX,
    ];
  }

  function drawDog(dg: DogView, camX: number, camY: number, myDog: bigint): void {
    if (!terrain) return;
    const cellX = Math.min(terrain.w - 1, Math.max(0, dg.xq >> 16));
    const cellY = Math.min(terrain.h - 1, Math.max(0, dg.yq >> 16));
    const i = cellY * terrain.w + cellX;
    const onDeck = (dg.flags & 1) !== 0;
    const swimming = (dg.flags & 2) !== 0;
    const moving = (dg.flags & 4) !== 0;
    const boosting = (dg.flags & 8) !== 0;
    const el = onDeck ? terrain.deck[i]! - 1 : terrain.elev[i]!;
    const [px, py] = worldToPx(dg.xq, dg.yq, el);
    const sx = px - camX;
    const sy = py - camY;
    const bob = moving && !swimming ? (dg.anim % 2) - 0.5 : 0;
    ctx.fillStyle = "rgba(0,0,0,0.25)";
    ctx.beginPath();
    ctx.ellipse(sx, sy + 1, 4, 2, 0, 0, Math.PI * 2);
    ctx.fill();
    if (boosting) {
      // dust puffs: the universal cartoon shorthand for "zoom"
      ctx.fillStyle = "rgba(255,224,138,0.6)";
      const k = dg.anim % 2 === 0 ? 1 : -1;
      ctx.beginPath();
      ctx.ellipse(sx - 6 * k, sy + 1, 2, 1.2, 0, 0, Math.PI * 2);
      ctx.ellipse(sx + 5 * k, sy + 2, 1.4, 1, 0, 0, Math.PI * 2);
      ctx.fill();
    }
    ctx.fillStyle = dg.id === myDog ? "#ffd166" : "#f4f1ea";
    ctx.beginPath();
    if (swimming) {
      ctx.ellipse(sx, sy - 1, 4, 2.2, 0, 0, Math.PI * 2);
      ctx.fill();
      ctx.strokeStyle = "rgba(255,255,255,0.5)";
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.ellipse(sx, sy, 6, 3, 0, 0, Math.PI * 2);
      ctx.stroke();
    } else {
      ctx.ellipse(sx, sy - 3 + bob, 3.4, 3.4, 0, 0, Math.PI * 2);
      ctx.fill();
    }
    if (dg.id === myDog) {
      ctx.strokeStyle = "#ffd166";
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.ellipse(sx, sy, 7, 3.5, 0, 0, Math.PI * 2);
      ctx.stroke();
    }
  }

  /** Reads the presented world, running every dog through the smoother in one batch. */
  function readDogs(bytes: Uint8Array, phaseQ16: number): DogView[] {
    const view = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength);
    const n = view.getUint32(0, true);
    if (quads.length < n * 4) quads = new Int32Array(n * 8);
    const dogs: DogView[] = [];
    for (let i = 0; i < n; i++) {
      const at = 4 + i * VIEW_STRIDE;
      const id = view.getBigUint64(at, true);
      const x = view.getInt32(at + 8, true); // Q16.16 cells
      const y = view.getInt32(at + 12, true);
      const [px, py] = prevPos.get(id.toString(16)) ?? [x, y];
      quads[i * 4] = px;
      quads[i * 4 + 1] = py;
      quads[i * 4 + 2] = x;
      quads[i * 4 + 3] = y;
      dogs.push({
        id,
        xq: x,
        yq: y,
        flags: view.getUint8(at + 16),
        anim: view.getUint8(at + 18),
      });
    }
    core.smooth(quads.subarray(0, n * 4), phaseQ16, SNAP_Q16);
    for (let i = 0; i < n; i++) {
      const dg = dogs[i]!;
      dg.xq = quads[i * 4]!;
      dg.yq = quads[i * 4 + 1]!;
      prevPos.set(dg.id.toString(16), [dg.xq, dg.yq]);
    }
    // drop smoothing state for dogs that left, so churn never accumulates
    if (prevPos.size > n * 2 + 16) {
      const seen = new Set(dogs.map((d) => d.id.toString(16)));
      for (const k of prevPos.keys()) if (!seen.has(k)) prevPos.delete(k);
    }
    return dogs;
  }

  return {
    followCamera: () => setCamFree(false),
    frame: (now) => {
      const view = core.view();
      if (!view) return;
      ctx.imageSmoothingEnabled = false;
      ctx.fillStyle = "#161a21";
      ctx.fillRect(0, 0, canvas.width, canvas.height);
      if (view.terrain !== terrain) {
        terrain = view.terrain;
        if (terrain) prerenderTerrain(terrain);
      }
      if (!terrain || !groundLayer || !deckLayer) return;

      const myDog = opts.myDog();
      const dogs = readDogs(view.viewBytes, view.phaseQ16);
      const names: string[] = [];
      let mine: DogView | null = null;
      for (const dg of dogs) {
        if (dg.id === myDog) {
          mine = dg;
          hud.diag.myX = dg.xq;
          hud.diag.myY = dg.yq;
        }
        if (names.length < 12) names.push(`walker-${dg.id.toString(16).slice(0, 4)}`);
      }
      // painter's order down the iso diagonal; layers handle under/over deck
      dogs.sort((a, b) => a.xq + a.yq - (b.xq + b.yq));

      const cdt = camT ? Math.min(100, now - camT) : 16;
      camT = now;
      if (camFree) {
        if (!dragging && (Math.abs(camV[0]) > 0.005 || Math.abs(camV[1]) > 0.005)) {
          cam[0] += camV[0] * cdt;
          cam[1] += camV[1] * cdt;
          const f = Math.pow(DECEL, cdt);
          camV[0] *= f;
          camV[1] *= f;
        }
      } else {
        const focus = mine ?? { xq: (terrain.w / 2) * Q16, yq: (terrain.h / 2) * Q16 };
        const [fx, fy] = worldToPx(focus.xq, focus.yq, 0);
        cam[0] = fx - canvas.width / 2;
        cam[1] = fy - canvas.height / 2;
      }
      const maxX = groundLayer.width - canvas.width;
      const maxY = groundLayer.height - canvas.height;
      if (cam[0] <= 0 || cam[0] >= maxX) camV[0] = 0;
      if (cam[1] <= 0 || cam[1] >= maxY) camV[1] = 0;
      cam[0] = Math.max(0, Math.min(maxX, cam[0]));
      cam[1] = Math.max(0, Math.min(maxY, cam[1]));
      const camX = cam[0];
      const camY = cam[1];
      lastCam = [camX, camY];
      hud.diag.camX = camX;
      hud.diag.camY = camY;
      hud.diag.camFree = camFree;

      ctx.drawImage(groundLayer, -camX, -camY);
      for (const dg of dogs) if (!(dg.flags & 1)) drawDog(dg, camX, camY, myDog);
      ctx.drawImage(deckLayer, -camX, -camY);
      for (const dg of dogs) if (dg.flags & 1) drawDog(dg, camX, camY, myDog);
      hud.setRoster(names, dogs.length);
      jank.sample(now, Number(view.tick), view.phaseQ16, mine, camX, camY, dogs);
    },
  };
}
