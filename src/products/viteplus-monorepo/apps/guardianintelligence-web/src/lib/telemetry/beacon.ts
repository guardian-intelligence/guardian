// Analytics beacon: drains the bounded emitSpan queue (browser.ts) and
// publishes Connect JSON batches to the same-origin ingest service. Loaded
// via dynamic import on idle — never on the critical path, never
// modulepreloaded (perf/beacon-budget.mjs asserts both). Loss semantics are
// deliberately best-effort: bounded queue, bounded localStorage replay,
// transport errors swallowed. Integrity lives server-side.

import type { TelemetryEvent } from "./browser";

const ENDPOINT = "/api/events/guardian.analytics.v1.EventService/Publish";
const LS_KEY = "guardian_events_v1";
const SEQ_KEY = "guardian_seq_v1";
const LS_CAP = 100;
const BATCH_CAP = 200; // server rejects > 500; stay well under
const BYTE_CAP = 60_000; // sendBeacon quota is 64KiB, shared

interface WireEvent {
  name: string;
  path?: string;
  referrer?: string;
  offsetMs?: number;
  sessionSeq?: number;
  vitalName?: string;
  vitalValue?: number;
  propsJson?: string;
}

let seq = 0;
let pending: WireEvent[] = [];
let timer: ReturnType<typeof setInterval> | undefined;
let started = false;
let replaying = false;

function lowData(): boolean {
  const c = (navigator as { connection?: { saveData?: boolean; effectiveType?: string } }).connection;
  return Boolean(c?.saveData) || /(^|-)2g$/.test(c?.effectiveType ?? "");
}

function loadSeq(): number {
  try {
    return Number(sessionStorage.getItem(SEQ_KEY)) || 0;
  } catch {
    return 0;
  }
}

function saveSeq(): void {
  try {
    sessionStorage.setItem(SEQ_KEY, String(seq));
  } catch {
    // storage denied: seq stays session-local
  }
}

function toWire(e: TelemetryEvent, nowPerf: number): WireEvent {
  const { "route.path": routePath, referrer, ...rest } = e.attrs;
  const w: WireEvent = {
    name: e.name,
    path: routePath ?? window.location.pathname,
    offsetMs: Math.max(0, Math.round(nowPerf - e.t)),
  };
  if (referrer) w.referrer = referrer;
  if (e.name.startsWith("web_vital.")) {
    w.vitalName = rest["web_vital.name"] ?? "";
    w.vitalValue = Number(rest["web_vital.value"]) || 0;
    delete rest["web_vital.name"];
    delete rest["web_vital.value"];
  }
  const keys = Object.keys(rest);
  if (keys.length > 0) {
    const json = JSON.stringify(rest);
    if (json.length <= 2000) w.propsJson = json;
  }
  return w;
}

function drain(): void {
  const q = window.__guardianEvents;
  if (!q?.length) return;
  const now = performance.now();
  const events = q.splice(0, q.length);
  const minimal = lowData();
  for (const e of events) {
    if (minimal && !(e.name === "company.route_view" || e.name.startsWith("web_vital."))) continue;
    const w = toWire(e, now);
    w.sessionSeq = ++seq;
    pending.push(w);
  }
  saveSeq();
}

function body(events: WireEvent[]): string {
  return JSON.stringify({ sentAtUnixMs: String(Date.now()), events });
}

// Split so no single request exceeds the event or byte cap.
function takeBatch(): WireEvent[] {
  const batch: WireEvent[] = [];
  let bytes = 64;
  while (pending.length > 0 && batch.length < BATCH_CAP) {
    const next = pending[0];
    if (next === undefined) break;
    const cost = JSON.stringify(next).length + 1;
    if (batch.length > 0 && bytes + cost > BYTE_CAP) break;
    batch.push(next);
    pending.shift();
    bytes += cost;
  }
  return batch;
}

function persist(events: WireEvent[]): void {
  try {
    const stored: WireEvent[] = JSON.parse(localStorage.getItem(LS_KEY) ?? "[]");
    // offsetMs is relative to THIS page's performance.now() origin; once it
    // survives to another page load that timeline is gone, so drop it —
    // session_seq still orders the events.
    const durable = events.map(({ offsetMs: _drop, ...rest }) => rest);
    const merged = stored.concat(durable);
    localStorage.setItem(LS_KEY, JSON.stringify(merged.slice(-LS_CAP)));
  } catch {
    // storage denied/full: drop (best-effort)
  }
}

function send(events: WireEvent[], sync: boolean): void {
  const payload = body(events);
  if (sync && "sendBeacon" in navigator) {
    const ok = navigator.sendBeacon(ENDPOINT, new Blob([payload], { type: "application/json" }));
    if (!ok) persist(events);
    return;
  }
  fetch(ENDPOINT, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: payload,
    keepalive: payload.length < 60_000,
    priority: "low",
  } as RequestInit).then(
    (res) => {
      if (!res.ok && res.status >= 500) persist(events);
    },
    () => persist(events),
  );
}

function flush(sync = false): void {
  drain();
  while (pending.length > 0) {
    send(takeBatch(), sync);
  }
}

function replay(): void {
  // 'online' can fire repeatedly on flaky links; without this guard each
  // firing re-POSTs the same batch (ingest does not dedup) before the first
  // success clears it.
  if (replaying) return;
  let stored: WireEvent[];
  try {
    stored = JSON.parse(localStorage.getItem(LS_KEY) ?? "[]");
  } catch {
    return;
  }
  if (!Array.isArray(stored) || stored.length === 0) return;
  replaying = true;
  const sentCount = Math.min(stored.length, BATCH_CAP);
  fetch(ENDPOINT, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: body(stored.slice(0, sentCount)),
    priority: "low",
  } as RequestInit).then(
    (res) => {
      replaying = false;
      if (!res.ok) return;
      // Re-read rather than removeItem: persist() only ever appends, so the
      // snapshot we sent is a prefix — drop exactly it, keeping anything
      // persisted concurrently during the request.
      try {
        const cur: WireEvent[] = JSON.parse(localStorage.getItem(LS_KEY) ?? "[]");
        const rest = cur.slice(sentCount);
        if (rest.length === 0) localStorage.removeItem(LS_KEY);
        else localStorage.setItem(LS_KEY, JSON.stringify(rest));
      } catch {
        // ignore
      }
    },
    () => {
      replaying = false; // still offline; next load/online retries
    },
  );
}

function arm(): void {
  if (timer !== undefined) clearInterval(timer);
  timer = setInterval(() => flush(false), lowData() ? 60_000 : 15_000);
}

export function initBeacon(): void {
  if (started || typeof window === "undefined") return;
  // Prerendered pages must not emit: gate on activation.
  if ((document as { prerendering?: boolean }).prerendering) {
    document.addEventListener("prerenderingchange", () => initBeacon(), { once: true });
    return;
  }
  started = true;
  seq = loadSeq();
  arm();
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "hidden") flush(true);
  });
  // pagehide fires where visibilitychange:hidden may not (some WebKit
  // paths); both flush, second is a no-op.
  window.addEventListener("pagehide", () => flush(true));
  window.addEventListener("pageshow", (e) => {
    if ((e as PageTransitionEvent).persisted) arm(); // bfcache restore
  });
  window.addEventListener("online", replay);
  replay();
}
