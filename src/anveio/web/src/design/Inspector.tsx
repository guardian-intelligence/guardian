// The right-hand panel: one text style's face, size and axes at one
// breakpoint, plus the font picker.
import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
} from "react";
import {
  CASES,
  CATEGORIES,
  FACE_KEYS,
  LOCAL_FONTS,
  num,
  phoneFace,
  STYLES,
  type Category,
  type Face,
  type FontInfo,
  type StyleId,
  type TextCase,
  type TypeScale,
} from "~/home/typeScale";
import {
  AXIS_NAMES,
  CATALOG,
  categoryName,
  fontInfo,
  isLocal,
  loadPreview,
  WEIGHT_NAMES,
} from "./fonts";
import type { Breakpoint } from "./frames";

export type InspectorProps = {
  scale: TypeScale;
  id: StyleId;
  bp: Breakpoint;
  setBp: (bp: Breakpoint) => void;
  count: number;
  changed: boolean;
  /** Merges `patch` into the style at the current breakpoint. */
  patch: (patch: Partial<Face>, tag: string) => void;
  /** Drops phone overrides, so phones inherit desktop again. */
  inherit: (keys: (keyof Face)[]) => void;
  revert: () => void;
  pickFamily: (font: FontInfo) => void;
};

const CASE_LABELS: Readonly<Record<TextCase, string>> = {
  none: "—",
  uppercase: "AG",
  lowercase: "ag",
  capitalize: "Ag",
};

export function Inspector(p: InspectorProps) {
  const style = p.scale.styles[p.id];
  const face = p.bp === "desktop" ? style.desktop : phoneFace(style);
  const font = fontInfo(face.family, p.scale);
  const meta = STYLES.find((s) => s.id === p.id);
  const [picking, setPicking] = useState(false);
  const [lockLh, setLockLh] = useState(true);
  const overrides = Object.keys(style.phone).length;

  // On phones a value is either inherited from desktop or overridden here.
  const own = (k: keyof Face) => p.bp === "desktop" || k in style.phone;
  const reset = (k: keyof Face) =>
    p.bp === "phone" && k in style.phone ? () => p.inherit([k]) : undefined;

  const weights = font ? (face.italic ? font.italic : font.roman) : [];
  const wght = font?.axes["wght"];
  const axes = Object.entries(font?.axes ?? {}).filter(([tag]) => tag !== "wght");

  return (
    <div className="dz-inspector-body">
      <header className="dz-ins-head">
        <div>
          <h2>{meta?.label}</h2>
          <code>{meta?.selector}</code>
        </div>
        <span className="dz-count">{p.count} on page</span>
      </header>

      <div className="dz-seg dz-bp" role="tablist" aria-label="Breakpoint">
        <button role="tab" aria-selected={p.bp === "desktop"} onClick={() => p.setBp("desktop")}>
          Desktop <small>&gt;860</small>
        </button>
        <button role="tab" aria-selected={p.bp === "phone"} onClick={() => p.setBp("phone")}>
          Phone <small>≤860</small>
          {overrides > 0 && <span className="dz-pill">{overrides}</span>}
        </button>
      </div>
      {p.bp === "phone" && (
        <p className="dz-note">Grey values follow desktop. Change one to override it on phones.</p>
      )}

      <Section title="Font">
        <div className={`dz-field ${own("family") ? "" : "inherited"}`}>
          <button className="dz-family" onClick={() => setPicking(true)}>
            <FontName font={font} family={face.family} />
            <span className="dz-family-src">
              {font ? (isLocal(face.family) ? "Anveio" : "Google") : "Unknown"}
            </span>
          </button>
          <Reset onReset={reset("family")} />
          {picking && (
            <FontPicker
              current={face.family}
              scale={p.scale}
              onPick={(f) => {
                p.pickFamily(f);
                setPicking(false);
              }}
              onClose={() => setPicking(false)}
            />
          )}
        </div>
        <div className="dz-row">
          <label className={`dz-field dz-grow ${own("weight") ? "" : "inherited"}`}>
            <span className="dz-label">Weight</span>
            <select
              value={weights.includes(face.weight) ? String(face.weight) : "custom"}
              onChange={(e) => p.patch({ weight: Number(e.target.value) }, "weight")}
            >
              {!weights.includes(face.weight) && <option value="custom">{num(face.weight)}</option>}
              {weights.map((w) => (
                <option key={w} value={w}>
                  {w} {WEIGHT_NAMES[w] ?? ""}
                </option>
              ))}
            </select>
            <Reset onReset={reset("weight")} />
          </label>
          <button
            className={`dz-toggle ${face.italic ? "on" : ""} ${own("italic") ? "" : "inherited"}`}
            disabled={!font || font.italic.length === 0 || font.roman.length === 0}
            aria-pressed={face.italic}
            title="Italic"
            onClick={() => {
              const list = !face.italic ? font?.italic : font?.roman;
              const weight =
                list && !list.includes(face.weight) && !wght
                  ? (list.find((w) => w >= face.weight) ?? list[list.length - 1] ?? face.weight)
                  : face.weight;
              p.patch({ italic: !face.italic, weight }, "italic");
            }}
          >
            <i>I</i>
          </button>
        </div>
        {wght ? (
          <Slider
            label="Weight"
            value={face.weight}
            min={wght[0]}
            max={wght[1]}
            step={1}
            inherited={!own("weight")}
            onChange={(v) => p.patch({ weight: v }, "weight")}
          />
        ) : (
          // A fixed-weight family: the slider steps through the weights it has.
          weights.length > 1 && (
            <Slider
              label="Weight"
              value={Math.max(0, weights.indexOf(face.weight))}
              min={0}
              max={weights.length - 1}
              step={1}
              format={(i) => String(weights[i] ?? face.weight)}
              inherited={!own("weight")}
              onChange={(i) => p.patch({ weight: weights[i] ?? face.weight }, "weight")}
            />
          )
        )}
      </Section>

      <Section title="Size">
        <div className="dz-row">
          <NumberField
            label="Size"
            unit="px"
            value={face.size}
            step={1}
            min={4}
            max={400}
            inherited={!own("size")}
            onReset={reset("size")}
            onChange={(size) =>
              p.patch(
                lockLh
                  ? {
                      size,
                      lineHeight: Math.round(((face.lineHeight * size) / face.size) * 100) / 100,
                    }
                  : { size },
                "size",
              )
            }
          />
          <NumberField
            label="Line"
            unit="px"
            value={face.lineHeight}
            step={0.5}
            min={1}
            max={800}
            inherited={!own("lineHeight")}
            onReset={reset("lineHeight")}
            hint={`×${num(Math.round((face.lineHeight / face.size) * 100) / 100)}`}
            onChange={(lineHeight) => p.patch({ lineHeight }, "lineHeight")}
          />
          <button
            className={`dz-toggle dz-lock ${lockLh ? "on" : ""}`}
            aria-pressed={lockLh}
            title="Scale line height with size"
            onClick={() => setLockLh(!lockLh)}
          >
            <LinkIcon />
          </button>
        </div>
        <div className="dz-row">
          <NumberField
            label="Tracking"
            unit="%"
            value={Math.round(face.tracking * 1000) / 10}
            step={0.1}
            min={-30}
            max={50}
            inherited={!own("tracking")}
            onReset={reset("tracking")}
            onChange={(v) => p.patch({ tracking: Math.round(v * 10) / 1000 }, "tracking")}
          />
          <div className={`dz-field dz-grow ${own("case") ? "" : "inherited"}`}>
            <span className="dz-label">Case</span>
            <div className="dz-seg dz-case">
              {CASES.map((c) => (
                <button
                  key={c}
                  aria-pressed={face.case === c}
                  title={c}
                  onClick={() => p.patch({ case: c }, "case")}
                >
                  {CASE_LABELS[c]}
                </button>
              ))}
            </div>
            <Reset onReset={reset("case")} />
          </div>
        </div>
      </Section>

      {axes.length > 0 && (
        <Section title="Variable axes">
          {axes.map(([tag, [min, max]]) => {
            const value = face.axes[tag];
            const auto = tag === "opsz" ? "Auto (follows size)" : "Default";
            return (
              <Slider
                key={tag}
                label={`${AXIS_NAMES[tag] ?? tag} · ${tag}`}
                value={value ?? (tag === "opsz" ? Math.min(max, Math.max(min, face.size)) : min)}
                unset={value === undefined ? auto : undefined}
                min={min}
                max={max}
                step={max - min > 10 ? 1 : 0.1}
                inherited={!own("axes")}
                onChange={(v) => p.patch({ axes: { ...face.axes, [tag]: v } }, `axis.${tag}`)}
                onClear={
                  value === undefined
                    ? undefined
                    : () => {
                        const { [tag]: _drop, ...rest } = face.axes;
                        p.patch({ axes: rest }, `axis.${tag}`);
                      }
                }
              />
            );
          })}
          {p.bp === "phone" && "axes" in style.phone && (
            <button className="dz-link" onClick={() => p.inherit(["axes"])}>
              Use desktop axes
            </button>
          )}
        </Section>
      )}

      <footer className="dz-ins-foot">
        {p.bp === "phone" && overrides > 0 && (
          <button
            className="dz-btn"
            onClick={() => p.inherit(FACE_KEYS.filter((k) => k in style.phone))}
          >
            Clear phone overrides
          </button>
        )}
        <button className="dz-btn" disabled={!p.changed} onClick={p.revert}>
          Revert to saved
        </button>
      </footer>
    </div>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="dz-section">
      <h3>{title}</h3>
      {children}
    </section>
  );
}

function Reset({ onReset }: { onReset: (() => void) | undefined }) {
  if (!onReset) return null;
  return (
    <button className="dz-reset" title="Follow desktop" onClick={onReset}>
      ↺
    </button>
  );
}

function FontName({ font, family }: { font: FontInfo | undefined; family: string }) {
  useEffect(() => {
    if (font) loadPreview(font);
  }, [font]);
  return (
    <span className="dz-font-name" style={{ fontFamily: `"${family}", var(--dz-ui)` }}>
      {family}
    </span>
  );
}

type NumberFieldProps = {
  label: string;
  unit: string;
  value: number;
  step: number;
  min: number;
  max: number;
  hint?: string;
  inherited: boolean;
  onReset?: (() => void) | undefined;
  onChange: (v: number) => void;
};

/** A number input whose label scrubs, like Figma's: drag it sideways. */
function NumberField(p: NumberFieldProps) {
  const [text, setText] = useState<string | null>(null);
  const clamp = (v: number) => Math.min(p.max, Math.max(p.min, Math.round(v / p.step) * p.step));
  const commit = (v: number) => {
    const next = Math.round(clamp(v) * 1000) / 1000;
    if (next !== p.value) p.onChange(next);
  };
  const scrub = (e: ReactPointerEvent<HTMLSpanElement>) => {
    const target = e.currentTarget;
    const start = e.clientX;
    const from = p.value;
    target.setPointerCapture(e.pointerId);
    const move = (m: PointerEvent) => {
      const steps = Math.round((m.clientX - start) / 3);
      commit(from + steps * p.step * (m.shiftKey ? 10 : 1));
    };
    const up = () => {
      target.removeEventListener("pointermove", move);
      target.removeEventListener("pointerup", up);
    };
    target.addEventListener("pointermove", move);
    target.addEventListener("pointerup", up);
  };
  return (
    // A div, not a label: the reset button would otherwise take the label's clicks.
    <div className={`dz-field dz-num ${p.inherited ? "inherited" : ""}`}>
      <span className="dz-num-head">
        <span className="dz-label dz-scrub" onPointerDown={scrub}>
          {p.label}
        </span>
        {p.hint && <span className="dz-hint">{p.hint}</span>}
        <Reset onReset={p.onReset} />
      </span>
      <span className="dz-num-box">
        <input
          aria-label={p.label}
          inputMode="decimal"
          value={text ?? num(p.value)}
          onFocus={(e) => {
            setText(num(p.value));
            e.target.select();
          }}
          onChange={(e) => {
            setText(e.target.value);
            const v = Number(e.target.value);
            if (e.target.value.trim() !== "" && Number.isFinite(v)) commit(v);
          }}
          onBlur={() => setText(null)}
          onKeyDown={(e) => {
            if (e.key === "ArrowUp" || e.key === "ArrowDown") {
              e.preventDefault();
              const v = p.value + (e.key === "ArrowUp" ? 1 : -1) * p.step * (e.shiftKey ? 10 : 1);
              commit(v);
              setText(num(clamp(v)));
            }
            if (e.key === "Enter" || e.key === "Escape") e.currentTarget.blur();
          }}
        />
        <span className="dz-unit">{p.unit}</span>
      </span>
    </div>
  );
}

type SliderProps = {
  label: string;
  value: number;
  min: number;
  max: number;
  step: number;
  inherited: boolean;
  unset?: string | undefined;
  format?: (v: number) => string;
  onChange: (v: number) => void;
  onClear?: (() => void) | undefined;
};

function Slider(p: SliderProps) {
  return (
    <label
      className={`dz-field dz-slider ${p.inherited ? "inherited" : ""} ${p.unset ? "unset" : ""}`}
    >
      <span className="dz-label">{p.label}</span>
      <input
        type="range"
        min={p.min}
        max={p.max}
        step={p.step}
        value={p.value}
        onChange={(e) => p.onChange(Number(e.target.value))}
      />
      <span className="dz-slider-value">{p.unset ?? (p.format ?? num)(p.value)}</span>
      {p.onClear && (
        <button className="dz-reset" title="Font default" onClick={p.onClear}>
          ×
        </button>
      )}
    </label>
  );
}

type FontPickerProps = {
  current: string;
  scale: TypeScale;
  onPick: (font: FontInfo) => void;
  onClose: () => void;
};

const LIMIT = 200;

/** Search across anveio.com's fonts and the Google Fonts catalog. */
export function FontPicker({ current, scale, onPick, onClose }: FontPickerProps) {
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState<Category | "all">("all");
  const [active, setActive] = useState(0);
  const list = useRef<HTMLDivElement>(null);

  const inUse = useMemo(() => {
    const used = new Set<string>();
    for (const s of Object.values(scale.styles)) {
      used.add(s.desktop.family);
      if (s.phone.family) used.add(s.phone.family);
    }
    return used;
  }, [scale]);

  const { rows, total } = useMemo(() => {
    const q = query.trim().toLowerCase();
    const match = (f: FontInfo) =>
      (category === "all" || f.category === category) && f.family.toLowerCase().includes(q);
    const local = LOCAL_FONTS.filter(match);
    const google = CATALOG.filter(match);
    return { rows: [...local, ...google.slice(0, LIMIT)], total: local.length + google.length };
  }, [query, category]);

  useEffect(() => setActive(0), [query, category]);

  // Load each name's preview glyphs as its row scrolls into view.
  useEffect(() => {
    const root = list.current;
    if (!root) return;
    const seen = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          const font = rows[Number(e.target.getAttribute("data-i"))];
          if (e.isIntersecting && font) loadPreview(font);
        }
      },
      { root, rootMargin: "200px" },
    );
    for (const el of root.querySelectorAll("[data-i]")) seen.observe(el);
    return () => seen.disconnect();
  }, [rows]);

  useEffect(() => {
    list.current?.querySelector(`[data-i="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  return (
    <>
      <div className="dz-scrim" onPointerDown={onClose} />
      <div className="dz-picker" role="dialog" aria-label="Choose a font">
        <input
          className="dz-search"
          autoFocus
          placeholder={`Search ${CATALOG.length + LOCAL_FONTS.length} fonts`}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "ArrowDown") setActive((a) => Math.min(rows.length - 1, a + 1));
            else if (e.key === "ArrowUp") setActive((a) => Math.max(0, a - 1));
            else if (e.key === "Enter" && rows[active]) onPick(rows[active]);
            else if (e.key === "Escape") onClose();
            else return;
            e.preventDefault();
          }}
        />
        <div className="dz-chips">
          {(["all", ...CATEGORIES] as const).map((c) => (
            <button key={c} aria-pressed={category === c} onClick={() => setCategory(c)}>
              {c === "all" ? "All" : categoryName(c)}
            </button>
          ))}
        </div>
        <div className="dz-picker-list" ref={list}>
          {rows.map((f, i) => (
            <button
              key={f.family}
              data-i={i}
              className={`dz-picker-row ${i === active ? "active" : ""} ${f.family === current ? "current" : ""}`}
              onPointerEnter={() => setActive(i)}
              onClick={() => onPick(f)}
            >
              <span className="dz-font-name" style={{ fontFamily: `"${f.family}", var(--dz-ui)` }}>
                {f.family}
              </span>
              <span className="dz-picker-meta">
                {inUse.has(f.family) && <span className="dz-pill">in use</span>}
                {isLocal(f.family) ? "Anveio" : categoryName(f.category)}
                {"wght" in f.axes ? " · variable" : ""}
              </span>
            </button>
          ))}
          {rows.length === 0 && <p className="dz-empty">No fonts match.</p>}
          {total > rows.length && (
            <p className="dz-empty">{total - rows.length} more; keep typing to narrow.</p>
          )}
        </div>
      </div>
    </>
  );
}

function LinkIcon() {
  return (
    <svg viewBox="0 0 16 16" width="14" height="14" aria-hidden="true">
      <path
        d="M6.5 9.5l3-3M7 4.5l1-1a2.5 2.5 0 013.5 3.5l-1 1M9 11.5l-1 1A2.5 2.5 0 014.5 9l1-1"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.3"
        strokeLinecap="round"
      />
    </svg>
  );
}
