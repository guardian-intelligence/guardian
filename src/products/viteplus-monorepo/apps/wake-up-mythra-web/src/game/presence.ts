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
        onDg(JSON.parse(dec.decode(value)));
      }
    } catch {
      /* stream torn down; the transport.closed handler redials */
    }
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
      const specs = players.filter((p) => p.role === "spectator").length;
      $("who").textContent =
        `dogs here: ${names.join(", ") || "none"}` + (specs ? ` · ${specs} watching` : "");
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
