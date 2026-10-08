import { useState } from "react";

import { STAGES, type Stage } from "../content.ts";
import { Orb, type OrbMode } from "../rumi/Orb.tsx";
import { CONTROLS, type Control, type Section } from "../tuning-schema.ts";
import { tuning, useTuning } from "./store.ts";

const SECTION_TITLES = {
  orb: "Rumi",
  glass: "Liquid Glass",
  ink: "Ink water",
} as const satisfies Record<Section, string>;
const SECTIONS = ["orb", "glass", "ink"] as const satisfies readonly Section[];
const MODES = ["idle", "listening", "thinking", "speaking"] as const satisfies readonly OrbMode[];
const MODE_LABELS = {
  idle: "Resting",
  listening: "Listening",
  thinking: "Working",
  speaking: "Speaking",
} as const satisfies Record<OrbMode, string>;

function Field({
  section,
  name,
  control,
  value,
}: {
  section: Section;
  name: string;
  control: Control;
  value: unknown;
}) {
  const id = `st-${section}-${name}`;
  const set = (v: string | number | boolean) => tuning.setField(section, name, v);
  if (control.kind === "color" && typeof value === "string") {
    return (
      <div className="st-field">
        <label htmlFor={id}>{control.label}</label>
        <span className="st-color">
          <input
            id={id}
            type="color"
            value={value}
            onChange={(e) => set(e.target.value.toUpperCase())}
          />
          <code>{value}</code>
        </span>
      </div>
    );
  }
  if (control.kind === "toggle" && typeof value === "boolean") {
    return (
      <div className="st-field">
        <label htmlFor={id}>{control.label}</label>
        <input id={id} type="checkbox" checked={value} onChange={(e) => set(e.target.checked)} />
      </div>
    );
  }
  if (control.kind === "range" && typeof value === "number") {
    return (
      <div className="st-field st-range">
        <label htmlFor={id}>{control.label}</label>
        <input
          id={id}
          type="range"
          min={control.min}
          max={control.max}
          step={control.step}
          value={value}
          onChange={(e) => set(Number(e.target.value))}
        />
        <output htmlFor={id}>{Number(value.toFixed(3))}</output>
      </div>
    );
  }
  return null;
}

type Props = {
  forced: Stage | null;
  onForce: (s: Stage | null) => void;
  onRestart: () => void;
  gpuError: string | null;
};

export function Studio({ forced, onForce, onRestart, gpuError }: Props) {
  const t = useTuning();
  const [mode, setMode] = useState<OrbMode>("speaking");
  const [muted, setMuted] = useState(false);
  const [status, setStatus] = useState("");
  const [open, setOpen] = useState<Record<Section, boolean>>({
    orb: true,
    glass: true,
    ink: false,
  });

  const flash = (msg: string) => {
    setStatus(msg);
    window.setTimeout(() => setStatus(""), 2500);
  };

  return (
    <aside className="studio" aria-label="Studio">
      <header className="st-head">
        <h2>Studio</h2>
        {status && <span className="st-status">{status}</span>}
      </header>

      {gpuError && <pre className="st-error">{gpuError}</pre>}

      <div className="st-group">
        <div className="st-field">
          <label htmlFor="st-stage">Frame</label>
          <select
            id="st-stage"
            value={forced ?? "live"}
            onChange={(e) =>
              onForce(
                e.target.value === "live"
                  ? null
                  : (STAGES.find((s) => s === e.target.value) ?? null),
              )
            }
          >
            <option value="live">Live prototype</option>
            {STAGES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </select>
        </div>
        {!forced && (
          <button type="button" className="st-btn" onClick={onRestart}>
            Restart the visit
          </button>
        )}
      </div>

      <div className="st-preview">
        <button
          type="button"
          className="st-orb"
          aria-label={muted ? "Unmute preview" : "Mute preview"}
          onClick={() => setMuted(!muted)}
        >
          <Orb mode={mode} muted={muted} size={168} />
        </button>
        <div className="st-modes" role="group" aria-label="Rumi's state">
          {MODES.map((m) => (
            <button key={m} type="button" aria-pressed={mode === m} onClick={() => setMode(m)}>
              {MODE_LABELS[m]}
            </button>
          ))}
        </div>
        <p className="st-hint">Tap her to preview muted.</p>
      </div>

      {SECTIONS.map((section) => {
        const values: Record<string, unknown> = t[section];
        return (
          <section key={section} className="st-group">
            <button
              type="button"
              className="st-section"
              aria-expanded={open[section]}
              onClick={() => setOpen({ ...open, [section]: !open[section] })}
            >
              {SECTION_TITLES[section]}
            </button>
            {open[section] &&
              Object.entries(CONTROLS[section]).map(([name, control]) => (
                <Field
                  key={name}
                  section={section}
                  name={name}
                  control={control}
                  value={values[name]}
                />
              ))}
          </section>
        );
      })}

      <footer className="st-actions">
        {import.meta.env.DEV && (
          <button
            type="button"
            className="st-btn st-primary"
            onClick={() =>
              tuning.saveAsDefaults().then(
                () => flash("Saved to src/tuning.ts"),
                (err: unknown) => flash(`Save failed: ${String(err)}`),
              )
            }
          >
            Save as defaults
          </button>
        )}
        <button
          type="button"
          className="st-btn"
          onClick={() =>
            navigator.clipboard.writeText(tuning.asTypeScript()).then(
              () => flash("Copied"),
              () => flash("Copy blocked"),
            )
          }
        >
          Copy as TS
        </button>
        <button type="button" className="st-btn" onClick={() => tuning.reset()}>
          Revert to saved
        </button>
      </footer>
    </aside>
  );
}
