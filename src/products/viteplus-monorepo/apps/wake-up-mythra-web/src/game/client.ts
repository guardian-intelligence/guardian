// The Wake Up Mythra replica client (docs/netcode.md): runs the identical
// park module the server runs, applies the journal's ordered events, and
// steps every tick locally — zero bytes per tick on the wire. Divergence
// is detected by pulled hash checks (cadence = the netcode-check-seconds
// flag), repaired by snapshot resync; late events roll back through a
// snapshot ring and replay. The page's HUD exposes the headline metric:
// downlink bytes per hour of screen-on time.

/* eslint-disable @typescript-eslint/no-explicit-any */

import { OpenFeature } from "@openfeature/web-sdk";
import { emitSpan, reportError } from "@guardian/telemetry";
import {
  accessToken,
  beginSignIn,
  completeSignIn,
  deviceId,
  popupRelay,
  subjectOf,
  type SignInOutcome,
} from "./auth";
import * as v from "valibot";

type Wasm = any;

const TICK_MS = 1000 / 24;
const TARGET_LAG_TICKS = 6; // replica trails server-now by 250ms
const RING_DEPTH = 10; // one snapshot per second of rollback depth
const EV_JOIN = 1;
const EV_CHECK_IN = 3;
const EV_MOVE_TO = 4;
const EV_EPOCH_ADVANCE = 6;
const REJECT_PRESENT = 2; // sim ERR_PRESENT
const REJECT_ABSENT = 3; // sim ERR_ABSENT

// Sim reject codes 1-9 and doorman codes 100-101 (rejectReasonName in
// mythrad session.go is the server-side mirror).
const REJECT_TEXT = {
  1: "the park couldn't read that intent",
  2: "your dog is already in the park",
  3: "your dog isn't in the park",
  4: "the park is full",
  5: "already checked in today",
  6: "the park doesn't know that action",
  7: "the park moved on to a new epoch",
  8: "your dog can't stand there",
  9: "the park's terrain is still loading",
  100: "spectators can't act — sign in to play",
  101: "that dog isn't yours",
} as const;
const RejectCode = v.picklist([1, 2, 3, 4, 5, 6, 7, 8, 9, 100, 101]);

// Snapshot layout mirrors the park module ("MYP2"): 60-byte header (day
// u32 @32, n u32 @36, energy u64 @40), then 30-byte dog records with
// checked_in_day u32 at offset +20. The sim_view stride is 20 bytes:
// id u64, x i32, y i32 (Q16.16 cells), flags u8, facing u8, anim u8, pad.
const SNAP_HEADER = 60;
const DOG_REC = 30;
const VIEW_STRIDE = 20;

const FNV_OFFSET = 0xcbf29ce484222325n;
const FNV_PRIME = 0x100000001b3n;
const MASK64 = (1n << 64n) - 1n;

function fnv64(s: string): bigint {
  let h = FNV_OFFSET;
  for (const b of new TextEncoder().encode(s)) {
    h ^= BigInt(b);
    h = (h * FNV_PRIME) & MASK64;
  }
  return h;
}

async function inflate(b64: string): Promise<Uint8Array> {
  const raw = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
  const stream = new Blob([raw]).stream().pipeThrough(new DecompressionStream("deflate-raw"));
  return new Uint8Array(await new Response(stream).arrayBuffer());
}

export function startGame(): void {
  const $ = (id: string) => document.getElementById(id) as HTMLElement;
  // Read after completeSignIn restores the pre-sign-in query, so ?park=
  // and ?spectate survive a redirect-flow round trip.
  let spectate = false;
  let parkName = "park-mythra";

  const diag = {
    bytesDown: 0,
    events: 0,
    checks: 0,
    mismatches: 0,
    resyncs: 0,
    rollbacks: 0,
    rejects: 0,
    tick: 0,
    startedAt: 0,
  };
  (globalThis as any).__mythraDiag = diag;

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

  // ---- replica state (survives redials) ----
  let sim: Wasm = null; // the park module instance
  let smoother: Wasm = null; // the presentation module
  // The module-epoch lane: an epoch_advance event (or a verdict whose park
  // hash disagrees) fetches the new module into a background instance;
  // the resync snapshot restores there and the instances swap between
  // frames (docs/netcode.md).
  let parkHash = ""; // display hash of the running park module
  let pendingSim: Wasm = null;
  let pendingHash = "";
  let sub = "";
  let myDog = 0n;
  // The active terrain artifact: raw planes for the renderer, loaded into
  // the sim before any restore. Content-addressed, so one fetch per hex;
  // the raw bytes stay cached to reload into swapped-in module instances.
  let terrainHex = "";
  let terrainRaw: Uint8Array | null = null;
  let loadedRawHex = "";
  let terrain: {
    w: number;
    h: number;
    ground: Uint8Array;
    elev: Uint8Array;
    deck: Uint8Array;
    obstacle: Uint8Array;
    variant: Uint8Array;
  } | null = null;
  let seq = 0; // last applied journal seq
  let connected = false;
  let anon = true; // anonymous spectator until a token proves otherwise
  let role = "spectator";

  // event queue (by seq), hash ring, snapshot ring, recent events for
  // rollback replay
  const pendingEvents: any[] = [];
  const hashRing = new Map<number, string>(); // tick -> hex hash
  const snapRing: { tick: number; seq: number; state: Uint8Array }[] = [];
  const recentEvents: any[] = [];
  let strikes = 0;
  let resyncing = false;

  // clock discipline
  let tickMs = TICK_MS;
  let acc = 0;
  let lastFrame = 0;
  let serverTick = 0; // from last verdict
  let serverTickAt = 0; // performance.now() at that verdict
  let rttMs = 100;

  // prediction overlay: own intents awaiting their journal event
  const pendingIntents = new Map<number, { kind: number; p: Uint8Array }>();
  // Intent identity must be unique across page loads for the same subject:
  // the server's idempotency window spans reconnects, so a counter that
  // restarts at 1 would collide with the previous load's ids and this
  // load's join would be swallowed as a resend. High bits are a per-load
  // nonce, low bits count; the whole id stays under 2^53 so a JSON number
  // carries it exactly.
  const intentNonce =
    ((crypto.getRandomValues(new Uint32Array(1))[0]! & 0xfffff) + 1) * 0x100000000;
  let intentCounter = 0;

  let dgWriter: any = null;
  let ctrlWriter: any = null;
  let transport: any = null;
  let backoff = 300;
  let dialEpoch = 0;

  // ---- wasm plumbing ----
  function ioWrite(bytes: Uint8Array): number {
    const mem = new Uint8Array(sim.memory.buffer);
    mem.set(bytes, sim.io_buf());
    return bytes.length;
  }
  function ioRead(len: number): Uint8Array {
    return new Uint8Array(sim.memory.buffer.slice(sim.io_buf(), sim.io_buf() + len));
  }
  function simTick(): number {
    return Number(sim.sim_tick());
  }
  function simHashHex(): string {
    return BigInt.asUintN(64, sim.sim_hash()).toString(16).padStart(16, "0");
  }
  function applyEvent(kind: number, payload: Uint8Array): number {
    const buf = new Uint8Array(2 + payload.length);
    buf[0] = kind & 0xff;
    buf[1] = kind >> 8;
    buf.set(payload, 2);
    return sim.sim_apply(ioWrite(buf));
  }

  // ensureTerrain fetches the named artifact, loads it into the sim's
  // terrain buffer, and decodes the planes for the renderer. Ordered
  // before any snapshot restore by onLine's sequential await. Never
  // throws: a failure would otherwise kill the stream reader while the
  // transport stays up, wedging the session with no visible symptom.
  async function ensureTerrain(hex: string | undefined): Promise<boolean> {
    if (!hex || hex === terrainHex || !sim) return true;
    try {
      let blob: Uint8Array;
      if (hex === loadedRawHex && terrainRaw) {
        blob = terrainRaw; // module swap: same world, new instance
      } else {
        const resp = await fetch(`/terrain/${hex}`);
        if (!resp.ok) throw new Error(`/terrain/${hex} ${resp.status}`);
        blob = new Uint8Array(await resp.arrayBuffer());
      }
      if (blob.length > sim.terrain_cap()) {
        throw new Error(`terrain ${hex} is ${blob.length}B, over module cap`);
      }
      const mem = new Uint8Array(sim.memory.buffer);
      mem.set(blob, sim.terrain_buf());
      const code = sim.sim_set_terrain(blob.length);
      if (code !== 0) throw new Error(`terrain ${hex} rejected (code ${code})`);
      terrainRaw = blob;
      loadedRawHex = hex;
      const dv = new DataView(blob.buffer);
      const w = dv.getUint16(8, true);
      const h = dv.getUint16(10, true);
      const cells = w * h;
      terrain = {
        w,
        h,
        ground: blob.subarray(16, 16 + cells),
        elev: blob.subarray(16 + cells, 16 + 2 * cells),
        deck: blob.subarray(16 + 2 * cells, 16 + 3 * cells),
        obstacle: blob.subarray(16 + 3 * cells, 16 + 5 * cells),
        variant: blob.subarray(16 + 5 * cells, 16 + 6 * cells),
      };
      terrainHex = hex;
      prerenderTerrain();
      logLine(`terrain ${hex.slice(0, 8)}: ${w}x${h}`);
      return true;
    } catch (e: any) {
      const id = reportError(e, { "error.op": "wum.terrain_load" });
      logLine(`terrain load failed: ${e?.message ?? e} [err ${id}]`);
      return false;
    }
  }

  // beginModuleSwap fetches the current park module into a background
  // instance and asks for a snapshot; the snapshot handler restores there
  // and swaps between frames. `sum8` (from the epoch_advance payload) is
  // the first 8 LE bytes of the expected sha256 — a fetch that disagrees
  // is logged and still adopted: the server only serves current bytes, and
  // any further flip arrives as its own epoch event.
  let fetchingModule = false;
  async function beginModuleSwap(why: string, sum8?: bigint) {
    if (fetchingModule) return;
    fetchingModule = true;
    try {
      const bytes = await fetch(`/behavior/park.wasm?v=${Date.now()}`).then((r) => {
        if (!r.ok) throw new Error(`park.wasm ${r.status}`);
        return r.arrayBuffer();
      });
      const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
      const hash = [...digest.slice(0, 4)].map((b) => b.toString(16).padStart(2, "0")).join("");
      if (hash === parkHash) return; // raced a flip that already settled
      const sum = new DataView(digest.buffer).getBigUint64(0, true);
      if (sum8 !== undefined && sum !== sum8) {
        logLine(`module fetch is ${hash}, not the announced epoch module — adopting anyway`);
      }
      pendingSim = (await WebAssembly.instantiate(bytes)).instance.exports;
      pendingHash = hash;
      logLine(`park module ${hash} staged (${why}); resyncing into it`);
      emitSpan("wum.module_swap", { "wum.hash": hash, "wum.why": why, "wum.park": parkName });
      requestResync(`module epoch (${why})`);
    } catch (e: any) {
      const id = reportError(e, { "error.op": "wum.module_swap" });
      logLine(`module swap failed: ${e?.message ?? e} [err ${id}]`);
      setTimeout(() => {
        fetchingModule = false;
        void beginModuleSwap(why, sum8);
      }, 3000);
      return;
    }
    fetchingModule = false;
  }

  function stepOnce() {
    sim.sim_step();
    const t = simTick();
    diag.tick = t;
    hashRing.set(t, simHashHex());
    if (hashRing.size > 30 * 24) {
      hashRing.delete(t - 30 * 24);
    }
    if (t % 24 === 0) {
      const len = sim.sim_snapshot();
      snapRing.push({ tick: t, seq, state: ioRead(len) });
      if (snapRing.length > RING_DEPTH) snapRing.shift();
      while (recentEvents.length && recentEvents[0].tick < (snapRing[0]?.tick ?? 0)) {
        recentEvents.shift();
      }
    }
  }

  // applyReady applies queued events at their exact tick; a late event
  // rolls back through the snapshot ring and replays (docs/netcode.md).
  function applyReady() {
    pendingEvents.sort((a, b) => a.seq - b.seq);
    while (pendingEvents.length && pendingEvents[0].seq <= seq) pendingEvents.shift();
    while (pendingEvents.length && pendingEvents[0].seq === seq + 1) {
      const ev = pendingEvents[0];
      if (ev.tick > simTick()) return; // future: apply when we get there
      if (ev.tick < simTick()) {
        rollbackTo(ev.tick);
        if (ev.tick < simTick()) {
          // deeper than the ring: resync instead
          requestResync("late event beyond rollback ring");
          return;
        }
      }
      pendingEvents.shift();
      const code = applyEvent(ev.kind, ev.payload);
      if (code !== 0) {
        requestResync(`event seq ${ev.seq} rejected locally (code ${code})`);
        return;
      }
      seq = ev.seq;
      diag.events++;
      recentEvents.push(ev);
      pendingIntents.delete(ev.intent);
      hashRing.set(simTick(), simHashHex());
      if (ev.kind === EV_EPOCH_ADVANCE && ev.payload.length === 12) {
        // The journaled boundary: ticks after this run on the new module.
        const sum8 = new DataView(ev.payload.buffer, ev.payload.byteOffset).getBigUint64(4, true);
        void beginModuleSwap(`epoch event seq ${ev.seq}`, sum8);
      }
    }
  }

  function rollbackTo(tick: number) {
    for (let i = snapRing.length - 1; i >= 0; i--) {
      const snap = snapRing[i]!;
      if (snap.tick <= tick) {
        const target = simTick();
        ioWrite(snap.state);
        if (sim.sim_restore(snap.state.length) !== 0) break;
        seq = snap.seq;
        diag.rollbacks++;
        // replay recent events and steps back to where we were
        const replay = recentEvents.filter((e) => e.seq > snap.seq);
        for (const e of replay) {
          while (simTick() < e.tick) stepOnce();
          if (applyEvent(e.kind, e.payload) === 0) seq = e.seq;
        }
        while (simTick() < target) stepOnce();
        return;
      }
    }
  }

  // ---- verdicts, checks, resync ----
  let checkTimer: ReturnType<typeof setInterval> | undefined;
  function scheduleChecks() {
    clearInterval(checkTimer);
    if (document.hidden || !connected) return; // radio silence when hidden
    const secs = OpenFeature.getClient().getNumberValue("netcode-check-seconds", 5);
    checkTimer = setInterval(sendCheck, secs * 1000);
  }
  document.addEventListener("visibilitychange", scheduleChecks);

  function sendCheck() {
    const t = simTick();
    const wh = hashRing.get(t);
    if (!wh || !dgWriter) return;
    diag.checks++;
    sendDg({ type: "check", tick: t, wh, ct: Date.now() });
  }

  function onVerdict(m: any) {
    rttMs = 0.8 * rttMs + 0.2 * Math.max(1, Date.now() - m.ct);
    serverTick = m.now + rttMs / 2 / TICK_MS;
    serverTickAt = performance.now();
    $("rtt").textContent = `${rttMs.toFixed(0)}ms`;
    // Backstop for a missed epoch_advance (attached mid-boundary): the
    // verdict names the server's park module; disagreement means swap.
    if (m.pw && parkHash && m.pw !== parkHash && !pendingSim) {
      void beginModuleSwap(`verdict says ${m.pw}`);
    }
    if (m.ok === undefined) {
      strike("check aged out of the server ring");
      return;
    }
    if (m.ok) {
      strikes = 0;
      $("world").textContent = "✓";
      return;
    }
    diag.mismatches++;
    emitSpan("wum.netcode_mismatch", { "wum.tick": String(m.tick), "wum.park": parkName });
    strike("world hash mismatch");
  }

  function strike(why: string) {
    if (++strikes >= 2) {
      strikes = 0;
      requestResync(why);
    }
  }

  function requestResync(why: string) {
    if (resyncing || !ctrlWriter) return;
    resyncing = true;
    resyncOnLanded = true;
    diag.resyncs++;
    emitSpan("wum.netcode_resync", {
      "wum.why": why,
      "wum.seq": String(seq),
      "wum.park": parkName,
    });
    $("world").textContent = "✗";
    logLine(`resync: ${why}`);
    sendCtrl({ type: "resync", have: seq });
  }

  // ---- protocol ----
  function sendCtrl(o: unknown) {
    ctrlWriter?.write(new TextEncoder().encode(JSON.stringify(o) + "\n")).catch(() => {});
  }
  function sendDg(o: unknown) {
    dgWriter?.write(new TextEncoder().encode(JSON.stringify(o))).catch(() => {});
  }

  function sendIntent(kind: number, payload: Uint8Array) {
    const id = intentNonce + ++intentCounter;
    pendingIntents.set(id, { kind, p: payload });
    sendCtrl({ type: "intent", id, kind, p: b64(payload) });
  }

  function b64(bytes: Uint8Array): string {
    return btoa(String.fromCharCode(...bytes));
  }
  function unb64(s: string): Uint8Array {
    return Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
  }

  function dogPayload(): Uint8Array {
    const p = new Uint8Array(8);
    new DataView(p.buffer).setBigUint64(0, myDog, true);
    return p;
  }

  async function onLine(m: any) {
    if (m.type === "welcome") {
      role = m.role;
      $("role").textContent = `${role} @ ${m.park}`;
      logLine(
        `welcome: ${role}, park ${m.park}, epoch ${m.epoch}, journal seq ${m.seq}, tick ${m.tick}`,
      );
      serverTick = m.tick;
      serverTickAt = performance.now();
      await ensureTerrain(m.terrain);
    } else if (m.type === "snapshot") {
      if (pendingSim) {
        // Swap between frames: the fresh instance becomes the replica and
        // this snapshot restores into it. Terrain reloads from the cached
        // raw bytes; the world hash check below vouches for the whole move.
        sim = pendingSim;
        pendingSim = null;
        parkHash = pendingHash;
        terrainHex = "";
        logLine(`park module ${parkHash} live`);
      }
      if (!(await ensureTerrain(m.terrain))) {
        // Without the terrain this snapshot cannot restore; unlatch and
        // ask again shortly — the retry loop is resync -> snapshot ->
        // ensureTerrain until the fetch heals.
        resyncing = false;
        setTimeout(() => requestResync("terrain fetch failed"), 3000);
        return;
      }
      const state = await inflate(m.z);
      ioWrite(state);
      const code = sim.sim_restore(state.length);
      if (code !== 0) {
        const id = reportError(new Error(`snapshot restore failed (code ${code})`), {
          "error.op": "wum.snapshot_restore",
        });
        logLine(`snapshot restore failed (code ${code}) [err ${id}]`);
        return;
      }
      seq = m.seq;
      pendingEvents.length = 0;
      recentEvents.length = 0;
      snapRing.length = 0;
      hashRing.clear();
      hashRing.set(simTick(), simHashHex());
      resyncing = false;
      const local = simHashHex();
      const okTxt = local === m.wh ? "✓" : "✗";
      $("world").textContent = okTxt;
      logLine(
        `state at seq ${seq}, tick ${m.tick}, wh ${m.wh.slice(0, 8)} ${okTxt} (${state.length}B raw)`,
      );
      // resend intents the journal has not answered
      for (const [id, it] of pendingIntents) {
        sendCtrl({ type: "intent", id, kind: it.kind, p: b64(it.p) });
      }
      if (resyncOnLanded) {
        resyncOnLanded = false;
        setTimeout(sendCheck, TICK_MS * 2);
      }
    } else if (m.type === "event") {
      pendingEvents.push({ ...m, payload: unb64(m.p ?? "") });
    } else if (m.type === "reject") {
      diag.rejects++;
      const it = pendingIntents.get(m.intent);
      pendingIntents.delete(m.intent);
      if (m.reason === REJECT_PRESENT && it?.kind === EV_JOIN) {
        return; // the dog is already in the park — that IS the joined state
      }
      const known = v.safeParse(RejectCode, m.reason);
      logLine(
        known.success
          ? REJECT_TEXT[known.output]
          : `intent ${m.intent} rejected (reason ${m.reason})`,
      );
      emitSpan("wum.netcode_reject", {
        "wum.reason": String(m.reason),
        "wum.kind": String(it?.kind ?? ""),
        "wum.intent": String(m.intent),
        "wum.park": parkName,
      });
      // "Absent" on a non-join intent means the park lost our dog (a
      // missed join or a departure that raced our reconnect): re-join and
      // retry the rejected intent behind it, at most once per window.
      if (
        m.reason === REJECT_ABSENT &&
        role === "player" &&
        it &&
        it.kind !== EV_JOIN &&
        Date.now() - lastAutoJoin > 5000
      ) {
        lastAutoJoin = Date.now();
        logLine("rejoining the park with your dog");
        sendIntent(EV_JOIN, dogPayload());
        sendIntent(it.kind, it.p);
      }
    }
  }
  let resyncOnLanded = false;
  let lastAutoJoin = 0;

  async function connect() {
    const myEpoch = ++dialEpoch;
    try {
      const q = new URLSearchParams({ park: parkName });
      const headers: Record<string, string> = {};
      if (!anon) {
        const token = await accessToken();
        if (!token) {
          // The refresh token aged out mid-session: degrade to anonymous
          // spectating and surface the way back in.
          logLine("session expired — spectating; sign in to rejoin with your dog");
          await setIdentity();
        } else {
          headers.Authorization = `Bearer ${token}`;
          if (spectate) q.set("spectate", "1");
        }
      }
      if (anon) {
        q.set("spectate", "1");
        q.set("device", deviceId());
      }
      const dialStart = performance.now();
      const mint = await fetch(`/session?${q}`, { method: "POST", headers });
      if (!mint.ok) throw new Error(`/session ${mint.status}`);
      const sess = await mint.json();
      const WT = (globalThis as any).WebTransport;
      transport = sess.certHashB64
        ? new WT(`https://${sess.endpoint}/wt`, {
            serverCertificateHashes: [
              {
                algorithm: "sha-256",
                value: Uint8Array.from(atob(sess.certHashB64), (c) => c.charCodeAt(0)).buffer,
              },
            ],
          })
        : new WT(`https://${sess.endpoint}/wt`);
      // A blocked UDP path can leave the handshake pending forever — ready
      // neither resolves nor rejects and closed never settles — so nothing
      // downstream would ever run or report. Race it against a deadline.
      let dialTimer: ReturnType<typeof setTimeout> | undefined;
      try {
        await Promise.race([
          transport.ready,
          new Promise<never>((_, reject) => {
            dialTimer = setTimeout(
              () => reject(new Error("WebTransport handshake timeout (10s)")),
              10_000,
            );
          }),
        ]);
      } catch (e) {
        try {
          transport.close();
        } catch {
          // already dead
        }
        throw e;
      } finally {
        clearTimeout(dialTimer);
      }
      if (myEpoch !== dialEpoch) {
        transport.close();
        return;
      }
      backoff = 300;
      failedDials = 0;
      connected = true;
      setStatus("connected");
      emitSpan("wum.connected", {
        "wum.park": parkName,
        "wum.role": role,
        "wum.anon": String(anon),
        "wum.dial_ms": String(Math.round(performance.now() - dialStart)),
      });
      if (!diag.startedAt) diag.startedAt = Date.now();

      const ctrl = await transport.createBidirectionalStream();
      ctrlWriter = ctrl.writable.getWriter();
      dgWriter = transport.datagrams.writable.getWriter();
      sendCtrl({
        type: "hello",
        proto: 3,
        ticket: sess.ticket,
        since_seq: seq,
        since_tick: simTick(),
      });
      readLines(ctrl.readable);
      readDatagrams(transport.datagrams.readable);
      if (role === "player") {
        sendIntent(EV_JOIN, dogPayload());
      }
      scheduleChecks();
      setTimeout(sendCheck, 1500);
      transport.closed
        .catch(() => {})
        .finally(() => {
          if (myEpoch === dialEpoch) onDead();
        });
    } catch (e: any) {
      const id = reportError(e, { "error.op": "wum.connect", "wum.park": parkName });
      logLine(`connect failed: ${e.message ?? e} [err ${id}]`);
      // After a few straight failures, say so where the roster would be —
      // dials keep retrying in the background, but the user should know the
      // park is unreachable, not empty (iOS WebKit's WebTransport
      // networking crash looks exactly like this).
      if (++failedDials === 3) {
        $("who").textContent =
          "Can't reach the park from this browser right now — the pack plays on without us. Still retrying.";
      }
      if (myEpoch === dialEpoch) onDead();
    }
  }
  let failedDials = 0;

  function onDead() {
    connected = false;
    clearInterval(checkTimer);
    setStatus("reconnecting");
    const wait = backoff;
    backoff = Math.min(backoff * 2, 5000);
    emitSpan("wum.redial", { "wum.backoff_ms": String(wait), "wum.park": parkName });
    logLine(`connection lost; redialing in ${wait}ms`);
    setTimeout(connect, wait + Math.random() * 250);
  }

  async function readLines(readable: ReadableStream<Uint8Array>) {
    const reader = readable.getReader();
    const dec = new TextDecoder();
    let buf = "";
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) return;
        diag.bytesDown += value.byteLength;
        buf += dec.decode(value, { stream: true });
        let i;
        while ((i = buf.indexOf("\n")) >= 0) {
          const line = buf.slice(0, i);
          buf = buf.slice(i + 1);
          if (line.trim()) await onLine(JSON.parse(line));
        }
      }
    } catch {
      /* torn down; transport.closed redials */
    }
  }

  async function readDatagrams(readable: ReadableStream<Uint8Array>) {
    const reader = readable.getReader();
    const dec = new TextDecoder();
    try {
      for (;;) {
        const { value, done } = await reader.read();
        if (done) return;
        diag.bytesDown += value.byteLength;
        const m = JSON.parse(dec.decode(value));
        if (m.type === "verdict") onVerdict(m);
      }
    } catch {
      /* torn down; transport.closed redials */
    }
  }

  // ---- the local tick loop ----
  function targetTick(now: number): number {
    if (!serverTickAt) return simTick();
    return serverTick + (now - serverTickAt) / TICK_MS - TARGET_LAG_TICKS;
  }

  function pump(now: number) {
    const dt = Math.min(250, now - lastFrame);
    lastFrame = now;
    if (!sim) return;
    acc += dt;
    const target = targetTick(now);
    const behind = target - simTick();
    // Slew, never jump: ±2% tick duration until converged.
    tickMs = TICK_MS * (behind > 1 ? 0.98 : behind < -1 ? 1.02 : 1);
    let budget = behind > 48 ? 2000 : 4; // headless fast-forward when far behind
    while (acc >= tickMs && simTick() < target && budget-- > 0) {
      acc -= tickMs;
      stepOnce();
      applyReady();
    }
    if (acc > 4 * tickMs) acc = 4 * tickMs;
    applyReady();
  }

  // ---- render: placeholder isometric view ----
  // 2:1 diamonds, 16x8 px tiles, 5 px per elevation level. The renderer is
  // a pure consumer of (terrain planes, sim_view buffer, camera) — the
  // seam a future standalone renderer package takes over wholesale.
  const Q16 = 65536;
  const TILE_W = 16;
  const TILE_H = 8;
  const ELEV_PX = 5;
  const ISO_PAD = TILE_H * 4; // top margin so raised tiles stay in-layer
  const canvas = $("grid") as HTMLCanvasElement;
  let groundLayer: HTMLCanvasElement | null = null;
  let deckLayer: HTMLCanvasElement | null = null;
  let isoOriginX = 0; // world-pixel x of cell (0,0)'s diamond center
  let lastCam: [number, number] = [0, 0];

  function shade(hex: string, f: number): string {
    const n = parseInt(hex.slice(1), 16);
    const c = (v: number) => Math.max(0, Math.min(255, Math.round(v * f)));
    return `rgb(${c(n >> 16)},${c((n >> 8) & 255)},${c(n & 255)})`;
  }

  function diamond(g: CanvasRenderingContext2D, cx: number, cy: number, fill: string) {
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
  function skirt(g: CanvasRenderingContext2D, cx: number, cy: number, depth: number, fill: string) {
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

  function prerenderTerrain() {
    if (!terrain) return;
    const { w, h } = terrain;
    isoOriginX = (h * TILE_W) / 2;
    const pw = ((w + h) * TILE_W) / 2 + TILE_W;
    const ph = ((w + h) * TILE_H) / 2 + ISO_PAD + TILE_H * 2;
    groundLayer = document.createElement("canvas");
    groundLayer.width = pw;
    groundLayer.height = ph;
    deckLayer = document.createElement("canvas");
    deckLayer.width = pw;
    deckLayer.height = ph;
    const g = groundLayer.getContext("2d")!;
    const d = deckLayer.getContext("2d")!;
    const GRASS = ["#79b356", "#7cb85c", "#76ad53", "#82bd63"];
    for (let y = 0; y < h; y++) {
      for (let x = 0; x < w; x++) {
        const i = y * w + x;
        const cls = terrain.ground[i]!;
        const el = terrain.elev[i]!;
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
          const v = terrain.variant[i]!;
          fill = v === 8 ? "#c9ad7c" : GRASS[v % 4]!; // path vs grass tufts
        }
        if (el > 0 || cls === 0) {
          const depth = (cls === 0 ? 1 : el) * ELEV_PX + (cls === 0 ? ELEV_PX : 0);
          // a waterfall face is the river pouring over its own ledge
          const southLower =
            cls === 2 && y + 1 < h && terrain.ground[i + w] === 2 && terrain.elev[i + w]! < el;
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
        const mask = terrain.obstacle[2 * i]! | (terrain.obstacle[2 * i + 1]! << 8);
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
        const dk = terrain.deck[i]!;
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

  type DogView = {
    id: bigint;
    xq: number;
    yq: number;
    flags: number;
    anim: number;
  };

  function drawDog(ctx: CanvasRenderingContext2D, dg: DogView, camX: number, camY: number) {
    if (!terrain) return;
    const cellX = Math.min(terrain.w - 1, Math.max(0, dg.xq >> 16));
    const cellY = Math.min(terrain.h - 1, Math.max(0, dg.yq >> 16));
    const i = cellY * terrain.w + cellX;
    const onDeck = (dg.flags & 1) !== 0;
    const swimming = (dg.flags & 2) !== 0;
    const moving = (dg.flags & 4) !== 0;
    const el = onDeck ? terrain.deck[i]! - 1 : terrain.elev[i]!;
    const [px, py] = worldToPx(dg.xq, dg.yq, el);
    const sx = px - camX;
    const sy = py - camY;
    const bob = moving && !swimming ? (dg.anim % 2) - 0.5 : 0;
    ctx.fillStyle = "rgba(0,0,0,0.25)";
    ctx.beginPath();
    ctx.ellipse(sx, sy + 1, 4, 2, 0, 0, Math.PI * 2);
    ctx.fill();
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

  const prevPos = new Map<string, [number, number]>();
  function frame(now: number) {
    requestAnimationFrame(frame);
    pump(now);
    if (!sim) return;
    const ctx = canvas.getContext("2d")!;
    ctx.imageSmoothingEnabled = false;
    ctx.fillStyle = "#161a21";
    ctx.fillRect(0, 0, canvas.width, canvas.height);
    $("tick").textContent = String(simTick());
    const hours = (Date.now() - (diag.startedAt || Date.now())) / 3600000;
    $("bytes").textContent =
      hours > 0.001 ? `${(diag.bytesDown / 1024 / hours).toFixed(1)}KB/h` : `${diag.bytesDown}B`;
    if (!terrain || !groundLayer || !deckLayer) return;
    const len = sim.sim_view();
    const view = new DataView(sim.memory.buffer, sim.io_buf(), len);
    const n = view.getUint32(0, true);
    const alpha = Math.min(1.25, Math.max(0, acc / tickMs));
    const names: string[] = [];
    const dogs: DogView[] = [];
    let mine: DogView | null = null;
    for (let i = 0; i < n; i++) {
      const at = 4 + i * VIEW_STRIDE;
      const id = view.getBigUint64(at, true);
      const x = view.getInt32(at + 8, true); // Q16.16 cells
      const y = view.getInt32(at + 12, true);
      const key = id.toString(16);
      const [px, py] = prevPos.get(key) ?? [x, y];
      let sx = x;
      let sy = y;
      if (smoother) {
        const fbuf = new Int32Array(smoother.memory.buffer, smoother.frame_buf(), 4);
        fbuf[0] = px;
        fbuf[1] = py;
        fbuf[2] = x;
        fbuf[3] = y;
        smoother.smooth_frame(1, (alpha * Q16) | 0, 8 * Q16);
        sx = fbuf[0]!;
        sy = fbuf[1]!;
      }
      prevPos.set(key, [sx, sy]);
      const dg: DogView = {
        id,
        xq: sx,
        yq: sy,
        flags: view.getUint8(at + 16),
        anim: view.getUint8(at + 18),
      };
      dogs.push(dg);
      if (id === myDog) mine = dg;
      if (names.length < 12) names.push(`walker-${key.slice(0, 4)}`);
    }
    // drop smoothing state for dogs that left, so churn never accumulates
    if (prevPos.size > n * 2 + 16) {
      const seen = new Set(dogs.map((d) => d.id.toString(16)));
      for (const k of prevPos.keys()) if (!seen.has(k)) prevPos.delete(k);
    }
    // painter's order down the iso diagonal; layers handle under/over deck
    dogs.sort((a, b) => a.xq + a.yq - (b.xq + b.yq));
    const focus = mine ?? {
      xq: (terrain.w / 2) * Q16,
      yq: (terrain.h / 2) * Q16,
      flags: 0,
    };
    const [fx, fy] = worldToPx(focus.xq, focus.yq, 0);
    const camX = Math.max(0, Math.min(groundLayer.width - canvas.width, fx - canvas.width / 2));
    const camY = Math.max(0, Math.min(groundLayer.height - canvas.height, fy - canvas.height / 2));
    lastCam = [camX, camY];
    ctx.drawImage(groundLayer, -camX, -camY);
    for (const dg of dogs) if (!(dg.flags & 1)) drawDog(ctx, dg, camX, camY);
    ctx.drawImage(deckLayer, -camX, -camY);
    for (const dg of dogs) if (dg.flags & 1) drawDog(ctx, dg, camX, camY);
    // The roster line only speaks for the park while we're attached to it —
    // a replica that never connected has zero dogs and would otherwise
    // report "the park is empty" to a user we simply couldn't reach.
    if (connected) {
      $("who").textContent =
        n === 0
          ? "the park is empty"
          : `dogs here: ${names.join(", ")}${n > names.length ? ` +${n - names.length} more` : ""}`;
    }
    // Park energy and our dog's check-in state for the HUD, straight from
    // the canonical snapshot: authoritative, and self-correcting through
    // rollback, resync, and day reset.
    if (simTick() % 24 === 0) {
      const sl = sim.sim_snapshot();
      if (sl >= SNAP_HEADER) {
        const hdr = new DataView(sim.memory.buffer, sim.io_buf(), SNAP_HEADER);
        $("energy").textContent = String(hdr.getBigUint64(40, true));
        if (role === "player") updateCheckinButton(sl, hdr.getUint32(32, true));
      }
    }
  }

  // A tap says "walk there": invert the iso projection at ground level and
  // send a move_to intent for the tapped cell.
  canvas.addEventListener("click", (e) => {
    if (!terrain || role !== "player") return;
    const rect = canvas.getBoundingClientRect();
    const px = ((e.clientX - rect.left) * canvas.width) / rect.width + lastCam[0];
    const py = ((e.clientY - rect.top) * canvas.height) / rect.height + lastCam[1];
    const wx = (px - isoOriginX) / (TILE_W / 2);
    const wy = (py - ISO_PAD) / (TILE_H / 2);
    const cellX = Math.floor((wx + wy) / 2);
    const cellY = Math.floor((wy - wx) / 2);
    if (cellX < 0 || cellY < 0 || cellX >= terrain.w || cellY >= terrain.h) return;
    const node = cellY * terrain.w + cellX;
    const p = new Uint8Array(10);
    const dv = new DataView(p.buffer);
    dv.setBigUint64(0, myDog, true);
    dv.setUint16(8, node, true);
    sendIntent(EV_MOVE_TO, p);
  });

  // Records are strictly sorted by dog id (snapshot encoding contract), so
  // our dog is a binary search away. checked_in_day === today's day index
  // means the sim would reject another check-in.
  function updateCheckinButton(snapLen: number, day: number) {
    const view = new DataView(sim.memory.buffer, sim.io_buf(), snapLen);
    const n = view.getUint32(36, true);
    let present = false;
    let checkedIn = false;
    let lo = 0;
    let hi = n - 1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      const at = SNAP_HEADER + mid * DOG_REC;
      const id = view.getBigUint64(at, true);
      if (id === myDog) {
        present = true;
        checkedIn = view.getUint32(at + 20, true) === day;
        break;
      }
      if (id < myDog) lo = mid + 1;
      else hi = mid - 1;
    }
    const btn = v.parse(v.instance(HTMLButtonElement), $("checkin"));
    btn.disabled = !present || checkedIn;
    btn.textContent = checkedIn ? "Checked in ✓" : "Check in";
  }

  // ---- boot ----
  // setIdentity resolves who is at the keyboard: a WUM account (player) or
  // the anonymous device pseudonym (spectator). Both get deterministic
  // feature flags and trace identity keyed on the sub; the device id rides
  // along after sign-in so the acquisition-funnel join survives the
  // upgrade.
  async function setIdentity(): Promise<void> {
    const token = await accessToken();
    anon = !token;
    sub = token ? subjectOf(token) : `anon:${deviceId()}`;
    role = anon || spectate ? "spectator" : "player";
    myDog = fnv64(sub);
    await OpenFeature.setContext({
      ...OpenFeature.getContext(),
      targetingKey: sub,
      device: deviceId(),
    });
    $("signin").style.display = anon ? "" : "none";
    const checkin = v.parse(v.instance(HTMLButtonElement), $("checkin"));
    checkin.style.display = role === "player" ? "" : "none";
    checkin.disabled = true; // until the snapshot shows our dog present
    checkin.textContent = "Check in";
  }

  async function onSignInOutcome(outcome: SignInOutcome): Promise<void> {
    if (outcome.status === "error") {
      emitSpan("wum.signin_failed", { "wum.reason": outcome.reason, "wum.flow": "popup" });
      logLine(`sign-in failed: ${outcome.reason}`);
      return;
    }
    if (outcome.status !== "signed-in") return;
    await setIdentity();
    emitSpan("wum.signin", { "wum.flow": "popup" });
    logLine("signed in — bringing your dog to the park");
    // Redial under the new identity; the replica state carries over, so
    // the upgrade is a reconnect, not a reload.
    dialEpoch++;
    try {
      transport?.close();
    } catch {
      /* already closed */
    }
    void connect();
  }

  async function boot() {
    if (popupRelay()) return;
    try {
      const outcome = await completeSignIn();
      spectate = new URLSearchParams(location.search).has("spectate");
      parkName = new URLSearchParams(location.search).get("park") || "park-mythra";
      if (outcome.status === "error") {
        emitSpan("wum.signin_failed", { "wum.reason": outcome.reason, "wum.flow": "redirect" });
        logLine(`sign-in failed: ${outcome.reason}`);
      } else if (outcome.status === "signed-in") {
        emitSpan("wum.signin", { "wum.flow": "redirect" });
      }
      $("signin").onclick = () => beginSignIn("google", (o) => void onSignInOutcome(o));
      await setIdentity();
      if (anon) {
        $("who").textContent = "You're watching the park live — sign in to bring your dog.";
      }

      const info = await (await fetch("/wt-info", { cache: "no-store" })).json();
      const [parkBytes, clientBytes] = await Promise.all([
        fetch(`/behavior/park.wasm?v=${info.parkWasm}`).then((r) => r.arrayBuffer()),
        fetch(`/behavior/client.wasm?v=${info.clientWasm}`).then((r) => r.arrayBuffer()),
      ]);
      sim = (await WebAssembly.instantiate(parkBytes)).instance.exports;
      smoother = (await WebAssembly.instantiate(clientBytes)).instance.exports;
      parkHash = info.parkWasm;
      logLine(`park module ${info.parkWasm}, presentation ${info.clientWasm}`);

      $("checkin").onclick = () => sendIntent(EV_CHECK_IN, dogPayload());

      if (typeof (globalThis as any).WebTransport === "undefined") {
        emitSpan("wum.unsupported", { "wum.feature": "webtransport" });
        setStatus("unsupported");
        $("who").textContent = "This browser can't reach the dog park (no WebTransport).";
        return;
      }
      logLine(role === "player" ? "joining with your dog" : "spectating — sign in to join");
      void connect();
    } catch (e: any) {
      const id = reportError(e, { "error.op": "wum.boot" });
      setStatus("error");
      $("who").textContent = `The park didn't load (${e?.message ?? e}) — refresh to retry.`;
      logLine(`boot failed: ${e?.message ?? e} [err ${id}]`);
    }
  }

  requestAnimationFrame((t) => {
    lastFrame = t;
    requestAnimationFrame(frame);
  });
  void boot();
}
