// The Wake Up Mythra presence client: dials the dog-park world over
// WebTransport, renders 24Hz server ticks smoothed to the display's frame
// rate by the client presentation module (Rust -> wasm, same structural core
// as the server sim), and verifies world state against the server's
// world-hash stamp. Game-service paths (/wt-info, /behavior/, /assets/) are
// same-origin: the apex Ingress routes them to the game service while this
// app serves the page itself.

/* eslint-disable @typescript-eslint/no-explicit-any */

type Sim = any;

export function startPresence(): void {
  const $ = (id: string) => document.getElementById(id) as HTMLElement;
  const store = sessionStorage;
  const spectate = new URLSearchParams(location.search).has("spectate");
  if (!store.getItem("name")) {
    store.setItem("name", "walker-" + Math.random().toString(16).slice(2, 6));
  }

  let transport: any = null;
  let dgWriter: any = null;
  let ctrlWriter: any = null;
  let backoff = 300;
  let epoch = 0;
  let pingTimer: ReturnType<typeof setInterval> | undefined;
  let myId: any = null;
  let lastBehavior: string | null = null;
  let players: any[] = [];
  const assetCache = new Map<string, HTMLImageElement | "loading">();

  // The client presentation module smooths raw tick positions and absorbs
  // device quirks (frame jank, tick jitter, resume teleports). Delivered
  // like server behaviors - a hash rides every pong, and a flip hot-swaps
  // the module mid-session.
  let sim: Sim = null;
  let simHash: string | null = null;
  let simLoading = false;
  let prevDogs = new Map<any, any[]>();
  let currDogs: any[] = [];
  let currAt = 0;
  let tickMs = 1000 / 24;
  let lastSt = 0;
  let roomSeed: string | null = null;
  let lastWorldOk: boolean | null = null;
  // Tick protocol v2: keyframes on the control stream establish the delta
  // roster (currDogs order); binary datagrams apply 4-bit movement codes
  // against it. kfSeq/kfOff gate stale or out-of-order deltas.
  let kfSeq: number | null = null;
  let kfOff = 0;
  let kfN = 0;
  const hiddenDogs = new Set<any>();
  const Q16 = 65536; // the sim is fixed-point Q16.16
  const SNAP_Q16 = 8 * Q16; // teleport threshold, cells

  async function loadClientSim(hash: string | undefined) {
    if (!hash || hash === simHash || simLoading) return;
    simLoading = true;
    try {
      const r = await fetch(`/behavior/client.wasm?v=${hash}`);
      if (!r.ok) throw new Error(`HTTP ${r.status}`);
      const { instance } = await WebAssembly.instantiate(await r.arrayBuffer());
      const prev = simHash;
      sim = instance.exports;
      simHash = hash;
      $("client").textContent = hash;
      if (prev) logLine(`client sim updated ${prev} → ${hash} — no reload, same session`);
      else logLine(`client sim ${hash} ready — smoothing at display rate`);
    } catch (e: any) {
      logLine(`client sim load failed (${e.message}); rendering raw ticks`);
    } finally {
      simLoading = false;
    }
  }

  function logLine(s: string) {
    const el = document.createElement("div");
    el.innerHTML = `<span class="t">${new Date().toTimeString().slice(0, 8)}</span> ${s}`;
    $("log").prepend(el);
    while ($("log").children.length > 80) $("log").lastChild?.remove();
  }
  function setStatus(s: string) {
    $("status").textContent = s.toUpperCase();
    $("status").className = "pill" + (s === "connected" ? " connected" : "");
  }

  async function connect() {
    const myEpoch = ++epoch;
    try {
      const info = await (await fetch("/wt-info", { cache: "no-store" })).json();
      loadClientSim(info.clientWasm);
      const WT = (globalThis as any).WebTransport;
      if (info.certHashB64) {
        // self-signed fallback: hash-pinned dial (Chromium-only)
        const hash = Uint8Array.from(atob(info.certHashB64), (c) => c.charCodeAt(0));
        transport = new WT(`https://${info.addr}/wt`, {
          serverCertificateHashes: [{ algorithm: "sha-256", value: hash.buffer }],
        });
      } else {
        // CA-issued cert: standards path, works wherever WebTransport does
        transport = new WT(`https://${info.addr}/wt`);
      }
      await transport.ready;
      if (myEpoch !== epoch) {
        transport.close();
        return;
      }
      backoff = 300;
      setStatus("connected");

      const ctrl = await transport.createBidirectionalStream();
      ctrlWriter = ctrl.writable.getWriter();
      dgWriter = transport.datagrams.writable.getWriter();
      sendCtrl({
        type: "hello",
        name: store.getItem("name"),
        device: deviceLabel(),
        token: store.getItem("token") || "",
        spectate,
        proto: 2,
      });
      readLines(ctrl.readable);
      readDatagrams(transport.datagrams.readable);
      clearInterval(pingTimer);
      pingTimer = setInterval(() => sendDg({ type: "ping", ct: Date.now() }), 1000);
      transport.closed
        .catch(() => {})
        .finally(() => {
          if (myEpoch === epoch) onDead();
        });
    } catch (e: any) {
      logLine(`connect failed: ${e.message ?? e}`);
      if (myEpoch === epoch) onDead();
    }
  }

  function deviceLabel() {
    return /iPhone|Android|Mobile/.test(navigator.userAgent) ? "phone" : "desktop";
  }

  function onDead() {
    clearInterval(pingTimer);
    setStatus(store.getItem("token") ? "resuming" : "connecting");
    const wait = backoff;
    backoff = Math.min(backoff * 2, 5000);
    logLine(`connection lost; redialing in ${wait}ms`);
    setTimeout(connect, wait + Math.random() * 250);
  }

  function sendCtrl(o: unknown) {
    ctrlWriter?.write(new TextEncoder().encode(JSON.stringify(o) + "\n")).catch(() => {});
  }
  function sendDg(o: unknown) {
    dgWriter?.write(new TextEncoder().encode(JSON.stringify(o))).catch(() => {});
  }

  async function readLines(readable: ReadableStream<Uint8Array>) {
    const reader = readable.getReader();
    const dec = new TextDecoder();
    let buf = "";
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) return;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf("\n")) >= 0) {
          const line = buf.slice(0, i);
          buf = buf.slice(i + 1);
          if (line.trim()) onCtrl(JSON.parse(line));
        }
      }
    } catch {
      /* stream torn down; the transport.closed handler redials */
    }
  }

  async function readDatagrams(readable: ReadableStream<Uint8Array>) {
    const reader = readable.getReader();
    const dec = new TextDecoder();
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) return;
        if (value[0] === 0xd7) onDelta(value);
        else onDg(JSON.parse(dec.decode(value)));
      }
    } catch {
      /* stream torn down; the transport.closed handler redials */
    }
  }

  // Binary movement delta: [0xD7, seq u32, off u16, start u16, count u16,
  // st u32] then 4-bit codes (dx+1)*3+(dy+1) low-nibble-first, indexed by
  // the keyframe roster order. 0xF marks a dog that left the room.
  function onDelta(buf: Uint8Array) {
    if (buf.length < 15 || kfSeq === null) return;
    const v = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
    const seq = v.getUint32(1, true);
    const off = v.getUint16(5, true);
    const start = v.getUint16(7, true);
    const count = v.getUint16(9, true);
    const st = v.getUint32(11, true);
    if (seq !== kfSeq || off < kfOff) return;
    diag.deltas++;
    if (off > kfOff) {
      // First chunk of a new tick: snapshot previous positions for the
      // smoother, advance the clock. Later chunks of the same off skip this.
      prevDogs = new Map(currDogs.map((d) => [d[0], [d[0], d[1], d[2]]]));
      kfOff = off;
      currAt = performance.now();
      const lo = lastSt >>> 0;
      const dt = (st - lo + 0x1_0000_0000) % 0x1_0000_0000;
      if (dt > 20 && dt < 200) tickMs = 0.9 * tickMs + 0.1 * dt;
      lastSt = st;
      $("tick").textContent = String(kfN + off);
    }
    for (let i = 0; i < count && start + i < currDogs.length; i++) {
      const byte = buf[15 + (i >> 1)]!;
      const code = i % 2 === 0 ? byte & 0x0f : byte >> 4;
      const dog = currDogs[start + i];
      if (code === 0x0f) {
        hiddenDogs.add(dog[0]);
        continue;
      }
      hiddenDogs.delete(dog[0]);
      dog[1] += ((code / 3) | 0) - 1;
      dog[2] += (code % 3) - 1;
    }
  }

  // Delta-lane diagnostics, exposed for harnesses and QA: every keyframe
  // is compared against the delta-accumulated state it replaces. On a
  // loss-free link the drift must be zero; sustained drift means the
  // decode disagrees with the sim.
  const diag = { kfChecks: 0, kfDrifted: 0, maxDrift: 0, deltas: 0 };
  (globalThis as any).__mythraDiag = diag;

  // A keyframe (control stream) is the authoritative snapshot: its dogs
  // array order is the roster deltas index into until the next keyframe.
  function onKeyframe(m: any) {
    if (kfSeq !== null && currDogs.length) {
      const prevById = new Map(currDogs.map((d) => [d[0], d]));
      let drift = 0;
      for (const d of m.dogs || []) {
        const o = prevById.get(d[0]);
        if (o && !hiddenDogs.has(d[0])) {
          drift = Math.max(drift, Math.abs(o[1] - d[1]) + Math.abs(o[2] - d[2]));
        }
      }
      diag.kfChecks++;
      if (drift > 0) diag.kfDrifted++;
      diag.maxDrift = Math.max(diag.maxDrift, drift);
    }
    if (m.behavior && m.behavior !== lastBehavior) {
      if (lastBehavior) {
        logLine(`sim behavior updated ${lastBehavior} → ${m.behavior} — no reload, same session`);
      }
      lastBehavior = m.behavior;
      $("behavior").textContent = m.behavior;
    }
    prevDogs = new Map(currDogs.map((d) => [d[0], [d[0], d[1], d[2]]]));
    currDogs = m.dogs || [];
    hiddenDogs.clear();
    for (const d of currDogs) if (d[3] === "gone") hiddenDogs.add(d[0]);
    kfSeq = m.seq;
    kfOff = m.off ?? 0;
    kfN = m.n - kfOff;
    currAt = performance.now();
    if (m.st) lastSt = m.st;
    $("tick").textContent = String(m.n);
    if (m.wh) verifyWorld(m);
  }

  function onCtrl(m: any) {
    if (m.type === "welcome") {
      myId = m.you.id;
      store.setItem("token", m.token);
      if (m.seed) roomSeed = m.seed;
      $("role").textContent = m.you.role + " @ " + m.you.device;
      logLine(`welcome: ${m.you.name} (${m.resumed ? "resumed" : "new"}), room ${m.you.room}`);
    } else if (m.type === "presence") {
      if (m.seed) roomSeed = m.seed;
      players = m.players;
      const names = players.filter((p) => p.role !== "spectator").map((p) => p.name);
      // The list is capped server-side; count/watching carry the totals.
      const total = m.count ?? names.length;
      const specs = m.watching ?? players.filter((p) => p.role === "spectator").length;
      const more = total > names.length ? ` +${total - names.length} more` : "";
      $("who").textContent =
        `dogs here: ${names.join(", ") || "none"}${more}` + (specs ? ` · ${specs} watching` : "");
    } else if (m.type === "keyframe") {
      onKeyframe(m);
    }
  }

  function onDg(m: any) {
    if (m.type === "pong") {
      $("rtt").textContent = Date.now() - m.ct + "ms";
      loadClientSim(m.cw);
      return;
    }
    if (m.type !== "tick") return;
    $("tick").textContent = m.n;
    if (m.behavior && m.behavior !== lastBehavior) {
      if (lastBehavior) {
        logLine(`sim behavior updated ${lastBehavior} → ${m.behavior} — no reload, same session`);
      }
      lastBehavior = m.behavior;
      $("behavior").textContent = m.behavior;
    }
    prevDogs = new Map(currDogs.map((d) => [d[0], d]));
    currDogs = m.dogs || [];
    currAt = performance.now();
    if (m.st && lastSt) {
      const dt = m.st - lastSt;
      if (dt > 20 && dt < 200) tickMs = 0.9 * tickMs + 0.1 * dt;
    }
    if (m.st) lastSt = m.st;
    if (m.wh) verifyWorld(m);
  }

  // The cross-surface oracle: recompute the world hash from our own snapshot
  // through the client module (the same core the server hashed with) and
  // verify it against the sim's stamp. A ✓ here proves this surface holds
  // bit-identical world state.
  function verifyWorld(m: any) {
    if (!sim?.wh_reset || !roomSeed) return;
    sim.wh_reset(BigInt(m.n), BigInt("0x" + roomSeed));
    const mem = new Uint8Array(sim.memory.buffer);
    const idPtr = sim.id_buf();
    for (const [id, x, y] of m.dogs || []) {
      const bytes = new TextEncoder().encode(String(id)).slice(0, 64);
      mem.set(bytes, idPtr);
      sim.wh_add(bytes.length, x, y);
    }
    const local = BigInt.asUintN(64, sim.wh_get()).toString(16).padStart(16, "0");
    const ok = local === m.wh;
    $("world").textContent = m.wh.slice(0, 8) + (ok ? " ✓" : " ✗");
    if (!ok && lastWorldOk !== false) {
      logLine(`world hash MISMATCH at tick ${m.n}: sim ${m.wh}, local ${local}`);
    }
    if (ok && lastWorldOk === false) logLine(`world hash re-verified at tick ${m.n}`);
    lastWorldOk = ok;
  }

  // asset streaming: unknown skin refs are fetched once by content address;
  // dogs render as a filled cell until the asset arrives.
  function skinImage(ref: string): HTMLImageElement | null {
    if (ref === "cell") return null;
    const cached = assetCache.get(ref);
    if (cached instanceof Image) return cached;
    if (cached === "loading") return null;
    assetCache.set(ref, "loading");
    logLine(`streaming asset ${ref}.svg…`);
    fetch(`/assets/${ref}.svg`)
      .then((r) => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        return r.blob();
      })
      .then((b) => {
        const img = new Image();
        img.onload = () => {
          assetCache.set(ref, img);
          $("skin").textContent = ref;
          logLine(`asset ${ref} ready`);
        };
        img.src = URL.createObjectURL(b);
      })
      .catch((e) => {
        assetCache.delete(ref);
        logLine(`asset ${ref} failed (${e.message}); retrying next tick`);
      });
    return null;
  }

  const ctx = ($("grid") as HTMLCanvasElement).getContext("2d")!;

  // One frame: positions come from the client sim when it's loaded (one
  // smooth_frame call over the whole dog buffer), raw ticks otherwise.
  function frame() {
    requestAnimationFrame(frame);
    const dogs = currDogs;
    ctx.fillStyle = "#FFFFFF";
    ctx.fillRect(0, 0, 100, 100);
    if (!dogs.length) return;
    let pos: Int32Array | null = null;
    if (sim) {
      const n = Math.min(dogs.length, sim.frame_cap());
      const buf = new Int32Array(sim.memory.buffer, sim.frame_buf(), n * 4);
      for (let i = 0; i < n; i++) {
        const [id, x, y] = dogs[i];
        const prev = prevDogs.get(id);
        buf[i * 4] = (prev ? prev[1] : x) * Q16;
        buf[i * 4 + 1] = (prev ? prev[2] : y) * Q16;
        buf[i * 4 + 2] = x * Q16;
        buf[i * 4 + 3] = y * Q16;
      }
      const alpha =
        Math.min(2 * Q16, Math.max(0, ((performance.now() - currAt) / tickMs) * Q16)) | 0;
      sim.smooth_frame(n, alpha, SNAP_Q16);
      pos = buf;
    }
    for (let i = 0; i < dogs.length; i++) {
      const [id, tx, ty, skin] = dogs[i];
      if (hiddenDogs.has(id)) continue;
      const x = pos && i * 4 < pos.length ? pos[i * 4]! / Q16 : tx;
      const y = pos && i * 4 < pos.length ? pos[i * 4 + 1]! / Q16 : ty;
      const img = skinImage(skin);
      if (img) {
        ctx.drawImage(img, x - 1.5, y - 1.5, 4, 4);
      } else {
        ctx.fillStyle = "#111111";
        ctx.fillRect(x, y, 1.6, 1.6);
      }
      if (id === myId) {
        // ring your own dog
        ctx.strokeStyle = "#111111";
        ctx.lineWidth = 0.4;
        ctx.strokeRect(x - 1.6, y - 1.6, 4.8, 4.8);
      }
    }
  }
  requestAnimationFrame(frame);

  if (typeof (globalThis as any).WebTransport === "undefined") {
    setStatus("unsupported");
    $("who").textContent =
      "This browser can't reach the dog park (no WebTransport). " +
      "Open this page in Chrome or Edge on desktop, or Chrome on Android. " +
      "iPhone support arrives with the app.";
    logLine("WebTransport unavailable in this browser");
  } else {
    logLine(spectate ? "spectating — your dog stays home" : "joining with your dog");
    connect();
  }
}
