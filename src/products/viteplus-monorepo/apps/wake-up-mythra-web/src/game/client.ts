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

type Wasm = any;

const TICK_MS = 1000 / 24;
const TARGET_LAG_TICKS = 6; // replica trails server-now by 250ms
const RING_DEPTH = 10; // one snapshot per second of rollback depth
const EV_JOIN = 1;
const EV_CHECK_IN = 3;
const EV_CHAT = 4;
const REJECT_PRESENT = 2; // sim ERR_PRESENT
const REJECT_ABSENT = 3; // sim ERR_ABSENT

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
  let sub = "";
  let myDog = 0n;
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
      if (ev.kind === EV_CHAT && ev.payload.length > 10) {
        const text = new TextDecoder().decode(ev.payload.subarray(10));
        logLine(`${shortName(ev.actor)}: ${text.slice(0, 140)}`);
      }
      hashRing.set(simTick(), simHashHex());
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

  function shortName(actor: string): string {
    return actor === "system" ? "system" : `walker-${fnv64(actor).toString(16).slice(0, 4)}`;
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
    } else if (m.type === "snapshot") {
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
      logLine(`intent ${m.intent} rejected (reason ${m.reason})`);
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
      await transport.ready;
      if (myEpoch !== dialEpoch) {
        transport.close();
        return;
      }
      backoff = 300;
      connected = true;
      setStatus("connected");
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
      if (myEpoch === dialEpoch) onDead();
    }
  }

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

  // ---- render ----
  const Q16 = 65536;
  const prevPos = new Map<string, [number, number]>();
  function frame(now: number) {
    requestAnimationFrame(frame);
    pump(now);
    if (!sim) return;
    const ctx = ($("grid") as HTMLCanvasElement).getContext("2d")!;
    ctx.fillStyle = "#FFFFFF";
    ctx.fillRect(0, 0, 100, 100);
    const len = sim.sim_view();
    const view = new DataView(sim.memory.buffer, sim.io_buf(), len);
    const n = view.getUint32(0, true);
    $("tick").textContent = String(simTick());
    const hours = (Date.now() - (diag.startedAt || Date.now())) / 3600000;
    $("bytes").textContent =
      hours > 0.001 ? `${(diag.bytesDown / 1024 / hours).toFixed(1)}KB/h` : `${diag.bytesDown}B`;
    const alpha = Math.min(1.25, Math.max(0, acc / tickMs));
    const names: string[] = [];
    for (let i = 0; i < n; i++) {
      const at = 4 + i * 16;
      const id = view.getBigUint64(at, true);
      const x = view.getInt32(at + 8, true);
      const y = view.getInt32(at + 12, true);
      const key = id.toString(16);
      const [px, py] = prevPos.get(key) ?? [x, y];
      let sx = x;
      let sy = y;
      if (smoother) {
        const fbuf = new Int32Array(smoother.memory.buffer, smoother.frame_buf(), 4);
        fbuf[0] = px * Q16;
        fbuf[1] = py * Q16;
        fbuf[2] = x * Q16;
        fbuf[3] = y * Q16;
        smoother.smooth_frame(1, (alpha * Q16) | 0, 8 * Q16);
        sx = fbuf[0]! / Q16;
        sy = fbuf[1]! / Q16;
      }
      prevPos.set(key, [sx, sy]);
      ctx.fillStyle = "#111111";
      ctx.fillRect(sx, sy, 1.6, 1.6);
      if (id === myDog) {
        ctx.strokeStyle = "#111111";
        ctx.lineWidth = 0.4;
        ctx.strokeRect(sx - 1.6, sy - 1.6, 4.8, 4.8);
      }
      if (names.length < 12) names.push(`walker-${key.slice(0, 4)}`);
    }
    $("who").textContent =
      n === 0
        ? "the park is empty"
        : `dogs here: ${names.join(", ")}${n > names.length ? ` +${n - names.length} more` : ""}`;
    // Park energy for the HUD straight from the canonical snapshot header.
    if (simTick() % 24 === 0) {
      const sl = sim.sim_snapshot();
      if (sl >= 48) {
        const hdr = new DataView(sim.memory.buffer, sim.io_buf(), 48);
        $("energy").textContent = String(hdr.getBigUint64(40, true));
      }
    }
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
    $("checkin").style.display = role === "player" ? "" : "none";
    ($("chat") as HTMLInputElement).style.display = role === "player" ? "" : "none";
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
      logLine(`park module ${info.parkWasm}, presentation ${info.clientWasm}`);

      $("checkin").onclick = () => sendIntent(EV_CHECK_IN, dogPayload());
      ($("chat") as HTMLInputElement).onkeydown = (e) => {
        if (e.key !== "Enter") return;
        const input = e.target as HTMLInputElement;
        const text = new TextEncoder().encode(input.value.slice(0, 140));
        if (!text.length) return;
        const p = new Uint8Array(10 + text.length);
        new DataView(p.buffer).setBigUint64(0, myDog, true);
        new DataView(p.buffer).setUint16(8, text.length, true);
        p.set(text, 10);
        sendIntent(EV_CHAT, p);
        input.value = "";
      };

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
