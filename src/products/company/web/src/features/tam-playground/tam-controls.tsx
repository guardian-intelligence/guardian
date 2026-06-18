import { useEffect, useId, useState } from "react";
import {
  TAM_LEVERS,
  formatLeverValue,
  leverValue,
  type TamLeverSpec,
  type TamProjectionInput,
} from "./model";

// The two-lever spec grew to four. Each lever is a labelled slider paired with
// a numeric input. The committed value always lives in the URL — these controls
// never hold the source of truth. The only local state is transient input text,
// so a half-typed "25" on the way to "250" doesn't get clamped out from under
// the reader's cursor.

export interface TamControlsProps {
  readonly input: TamProjectionInput;
  // Live update: write the value into URL search so the chart/table recompute
  // immediately as the slider drags.
  readonly onLeverChange: (spec: TamLeverSpec, value: number) => void;
  // Settle: a lever has come to rest (pointer up / blur / enter). This is the
  // telemetry-worthy "lever_change", as opposed to every intermediate frame.
  readonly onLeverSettle: (spec: TamLeverSpec, value: number) => void;
  readonly onReset: () => void;
  readonly onDownloadCsv: () => void;
  readonly isModified: boolean;
}

export function TamControls({
  input,
  onLeverChange,
  onLeverSettle,
  onReset,
  onDownloadCsv,
  isModified,
}: TamControlsProps) {
  return (
    <div className="flex flex-col gap-6" data-tam-controls>
      <div className="grid grid-cols-1 gap-x-8 gap-y-6 sm:grid-cols-2">
        {TAM_LEVERS.map((spec) => (
          <LeverControl
            key={spec.param}
            spec={spec}
            value={leverValue(input, spec)}
            onChange={onLeverChange}
            onSettle={onLeverSettle}
          />
        ))}
      </div>
      <div
        className="flex flex-wrap items-center gap-x-6 gap-y-2 pt-1"
        style={{ borderTop: "1px solid var(--treatment-hairline)", paddingTop: "16px" }}
      >
        <ToolbarButton onClick={onDownloadCsv}>Download CSV</ToolbarButton>
        <ToolbarButton onClick={onReset} disabled={!isModified}>
          Reset to scenario
        </ToolbarButton>
      </div>
    </div>
  );
}

function LeverControl({
  spec,
  value,
  onChange,
  onSettle,
}: {
  spec: TamLeverSpec;
  value: number;
  onChange: (spec: TamLeverSpec, value: number) => void;
  onSettle: (spec: TamLeverSpec, value: number) => void;
}) {
  const id = useId();
  // Transient text mirrors the committed value but lets the reader type freely.
  const [draft, setDraft] = useState<string>(String(value));
  // When the URL value changes from elsewhere (slider, reset, shared link),
  // resync the text. The reader's own keystrokes set draft directly, so this
  // only fights them on an actual external change.
  useEffect(() => {
    setDraft(String(value));
  }, [value]);

  const commitNumber = (raw: string, settle: boolean) => {
    const parsed = Number(raw);
    if (!Number.isFinite(parsed)) return;
    onChange(spec, parsed);
    if (settle) onSettle(spec, parsed);
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between gap-3">
        <label
          htmlFor={id}
          style={{
            fontFamily: "'Geist', sans-serif",
            fontSize: "13px",
            fontWeight: 500,
            color: "var(--treatment-ink)",
          }}
        >
          {spec.label}
        </label>
        <span
          className="font-mono"
          style={{
            fontSize: "13px",
            color: "var(--treatment-muted)",
            fontVariantNumeric: "tabular-nums",
          }}
        >
          {formatLeverValue(spec, value)}
        </span>
      </div>
      <div className="flex items-center gap-3">
        <input
          id={id}
          type="range"
          min={spec.min}
          max={spec.max}
          step={spec.step}
          value={value}
          aria-label={`${spec.label} (${spec.unit})`}
          onChange={(event) => onChange(spec, Number(event.target.value))}
          onPointerUp={(event) => onSettle(spec, Number(event.currentTarget.value))}
          onKeyUp={(event) => onSettle(spec, Number(event.currentTarget.value))}
          className="h-1 flex-1 cursor-pointer appearance-none rounded-full"
          style={{
            accentColor: "var(--treatment-ink)",
            background: "var(--treatment-hairline)",
          }}
        />
        <input
          type="number"
          min={spec.min}
          max={spec.max}
          step={spec.step}
          value={draft}
          aria-label={`${spec.label} value (${spec.unit})`}
          onChange={(event) => {
            setDraft(event.target.value);
            commitNumber(event.target.value, false);
          }}
          onBlur={(event) => commitNumber(event.target.value, true)}
          onKeyDown={(event) => {
            if (event.key === "Enter") commitNumber(event.currentTarget.value, true);
          }}
          className="w-20 rounded-sm px-2 py-1 font-mono"
          style={{
            fontSize: "13px",
            color: "var(--treatment-ink)",
            border: "1px solid var(--treatment-hairline)",
            background: "transparent",
            fontVariantNumeric: "tabular-nums",
          }}
        />
      </div>
      <p
        style={{
          fontFamily: "'Geist', sans-serif",
          fontSize: "11.5px",
          lineHeight: 1.4,
          color: "var(--treatment-muted-meta)",
          margin: 0,
        }}
      >
        {spec.help}
      </p>
    </div>
  );
}

function ToolbarButton({
  children,
  onClick,
  disabled,
}: {
  children: React.ReactNode;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className="font-mono text-[11px] font-semibold uppercase transition-opacity disabled:opacity-40"
      style={{
        color: "var(--treatment-ink)",
        letterSpacing: "0.12em",
        textUnderlineOffset: "4px",
        textDecoration: "underline",
        cursor: disabled ? "default" : "pointer",
        background: "none",
        border: "none",
        padding: 0,
      }}
    >
      {children}
    </button>
  );
}
