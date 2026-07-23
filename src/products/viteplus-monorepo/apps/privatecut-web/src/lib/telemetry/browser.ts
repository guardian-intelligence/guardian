// Client event capture. emitSpan is the app-wide emission interface (name +
// flat string attrs); every component keeps calling it unchanged. Events land
// in a bounded window queue that the analytics beacon drains; until the beacon
// module loads (or if it never does), the queue is capped and drop-oldest, so
// this file costs no network and no third-party code on the critical path.

export interface TelemetryEvent {
  readonly name: string;
  readonly attrs: Record<string, string>;
  // performance.now() at emission, for offset_ms relative to batch send time.
  readonly t: number;
}

declare global {
  interface Window {
    __guardianEvents?: TelemetryEvent[];
  }
}

const MAX_QUEUE = 100;

export function emitSpan(name: string, attrs: Record<string, string>): void {
  if (typeof window === "undefined") return;
  const queue = (window.__guardianEvents ??= []);
  queue.push({ name, attrs, t: performance.now() });
  if (queue.length > MAX_QUEUE) {
    queue.splice(0, queue.length - MAX_QUEUE);
  }
}
