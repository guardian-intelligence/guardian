import { useSyncExternalStore } from "react";
import * as v from "valibot";

import { DEFAULT_TUNING } from "../tuning.ts";
import { TuningSchema, type Section, type Tuning } from "../tuning-schema.ts";

// Live tuning shared by the studio, the React tree and the render loops.
// The working draft survives reloads in localStorage; "Save as defaults"
// writes it into src/tuning.ts.

const DRAFT_KEY = "anveio-cal:tuning-draft";

let defaults: Tuning = DEFAULT_TUNING;
const saved = (): Tuning => structuredClone(defaults);

const DraftShape = v.object({
  orb: v.record(v.string(), v.unknown()),
  glass: v.record(v.string(), v.unknown()),
  ink: v.record(v.string(), v.unknown()),
});

function readDraft(): Tuning | null {
  try {
    const raw = localStorage.getItem(DRAFT_KEY);
    if (!raw) return null;
    // Layer the draft over the saved defaults so levers added since it was
    // written pick up their defaults instead of invalidating the whole draft.
    const draft: unknown = JSON.parse(raw);
    const base = saved();
    const merged = v.is(DraftShape, draft)
      ? {
          orb: { ...base.orb, ...draft.orb },
          glass: { ...base.glass, ...draft.glass },
          ink: { ...base.ink, ...draft.ink },
        }
      : draft;
    const parsed = v.safeParse(TuningSchema, merged);
    return parsed.success ? parsed.output : null;
  } catch {
    return null;
  }
}

function writeDraft(t: Tuning | null): void {
  try {
    if (t) localStorage.setItem(DRAFT_KEY, JSON.stringify(t));
    else localStorage.removeItem(DRAFT_KEY);
  } catch {
    // storage blocked: the draft just won't survive a reload
  }
}

let current: Tuning = readDraft() ?? saved();
const listeners = new Set<() => void>();

function publish(next: Tuning, persist: boolean): void {
  current = next;
  if (persist) writeDraft(next);
  listeners.forEach((l) => l());
}

export const tuning = {
  get: (): Tuning => current,
  subscribe: (listener: () => void): (() => void) => {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  /** Set one lever; anything the schema rejects is ignored. */
  setField(section: Section, key: string, value: unknown): void {
    const next = v.safeParse(TuningSchema, {
      ...current,
      [section]: { ...current[section], [key]: value },
    });
    if (next.success) publish(next.output, true);
  },
  /** Drop the draft and go back to src/tuning.ts. */
  reset(): void {
    writeDraft(null);
    publish(saved(), false);
  },
  hasDraft: (): boolean => readDraft() !== null,
  async saveAsDefaults(): Promise<void> {
    const res = await fetch("/__studio/tuning", { method: "POST", body: JSON.stringify(current) });
    if (!res.ok) throw new Error(await res.text());
    writeDraft(null);
  },
  asTypeScript: (): string =>
    `export const rumiTuning = ${JSON.stringify(current, null, 2)} as const;\n`,
};

// Saving (or hand-editing) src/tuning.ts updates the defaults in place; with no
// draft in progress the page adopts them, without reloading anything downstream.
import.meta.hot?.accept("../tuning.ts", (mod) => {
  const parsed = v.safeParse(TuningSchema, mod?.["DEFAULT_TUNING"]);
  if (!parsed.success) return;
  defaults = parsed.output;
  if (!readDraft()) publish(saved(), false);
});

export function useTuning(): Tuning {
  return useSyncExternalStore(tuning.subscribe, tuning.get);
}
