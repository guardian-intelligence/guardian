// Flight recorder for the one failure telemetry cannot see on its own: an
// encode its page never finished. emitSpan queues in memory and the beacon
// drains every 15s, so a tab that dies mid-encode (renderer or GPU process
// crash, OOM kill) takes its last events with it. Each in-flight encode is
// mirrored into localStorage with a heartbeat instead, and a later page load
// reports what a dead page left behind as `privatecut.encode_abandoned`:
// `page_left` when that page saw a pagehide mid-encode (navigated away or
// reloaded — often giving up on a stall), `page_lost` when it never did
// (crashed or killed).

import { emitSpan, pageIdHex } from "@guardian/telemetry";

const PREFIX = "privatecut_encode_inflight_v1:";
const HEARTBEAT_MS = 5_000;
// A record whose heartbeat is older than this belongs to a dead page; a
// fresher one may be another tab still encoding, so it is re-checked later.
const STALE_MS = 15_000;

interface InflightEncode {
  readonly id: string;
  readonly startedAt: number;
  readonly attrs: Record<string, string>;
  readonly updatedAt: number;
  readonly pass: number;
  readonly fraction: number;
  readonly hidden: boolean;
  readonly contextLost: boolean;
}

const key = PREFIX + pageIdHex;
let current: InflightEncode | null = null;
let heartbeat: number | null = null;
let installed = false;

function write(next: InflightEncode | null): void {
  current = next;
  try {
    if (next === null) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(next));
  } catch {
    // storage denied/full: the journal is evidence, not a dependency
  }
}

function touch(patch: Partial<InflightEncode>): void {
  if (current !== null) write({ ...current, ...patch, updatedAt: Date.now() });
}

export function startEncodeRecord(id: string, attrs: Record<string, string>): void {
  const now = Date.now();
  write({
    id,
    startedAt: now,
    attrs,
    updatedAt: now,
    pass: 1,
    fraction: 0,
    hidden: false,
    contextLost: false,
  });
  if (heartbeat !== null) window.clearInterval(heartbeat);
  heartbeat = window.setInterval(() => touch({}), HEARTBEAT_MS);
}

// Progress lands here many times a second; the heartbeat persists it, so
// only a pass change is written through immediately.
export function noteEncodeProgress(pass: number, fraction: number): void {
  if (current === null) return;
  if (pass !== current.pass) touch({ pass, fraction });
  else current = { ...current, fraction };
}

export function finishEncodeRecord(): void {
  if (heartbeat !== null) window.clearInterval(heartbeat);
  heartbeat = null;
  write(null);
}

// The canvas compositor's WebGL context is lost when the GPU process dies,
// which is also what takes hardware codecs down. Reported whether or not an
// encode is running, joined to the encode when one is.
export function noteGpuContextLost(): void {
  emitSpan("privatecut.gpu_context_lost", {
    encoding: String(current !== null),
    ...(current !== null && {
      "trace.id": current.id,
      pass: String(current.pass),
      fraction: current.fraction.toFixed(3),
    }),
  });
  touch({ contextLost: true });
}

function readForeign(): Array<{ storageKey: string; record: InflightEncode }> {
  const found: Array<{ storageKey: string; record: InflightEncode }> = [];
  try {
    for (let index = 0; index < localStorage.length; index += 1) {
      const storageKey = localStorage.key(index);
      if (storageKey === null || !storageKey.startsWith(PREFIX) || storageKey === key) continue;
      const raw = localStorage.getItem(storageKey);
      if (raw === null) continue;
      found.push({ storageKey, record: JSON.parse(raw) as InflightEncode });
    }
  } catch {
    // unreadable storage: nothing to recover
  }
  return found;
}

function reportAbandoned(storageKey: string, record: InflightEncode): void {
  emitSpan("privatecut.encode_abandoned", {
    ...record.attrs,
    "trace.id": record.id,
    reason: record.hidden ? "page_left" : "page_lost",
    pass: String(record.pass),
    fraction: record.fraction.toFixed(3),
    ran_ms: String(Math.max(record.updatedAt - record.startedAt, 0)),
    context_lost: String(record.contextLost),
  });
  try {
    localStorage.removeItem(storageKey);
  } catch {
    // reported once already; a duplicate on a later load is acceptable
  }
}

function sweep(deferFresh: boolean): void {
  let deferred = false;
  for (const { storageKey, record } of readForeign()) {
    if (record.hidden || Date.now() - record.updatedAt > STALE_MS) {
      reportAbandoned(storageKey, record);
    } else if (deferFresh && !deferred) {
      // Possibly another live tab: look again once its heartbeat would
      // have advanced.
      deferred = true;
      window.setTimeout(() => sweep(false), STALE_MS + HEARTBEAT_MS);
    }
  }
}

export function installEncodeJournal(): void {
  if (installed || typeof window === "undefined") return;
  installed = true;
  sweep(true);
  window.addEventListener("pagehide", () => touch({ hidden: true }));
  window.addEventListener("pageshow", (event) => {
    if (event.persisted) touch({ hidden: false });
  });
}
