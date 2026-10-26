/* /design: a Figma-like twin of the home page for its type. The real page
   runs in frames at device widths; clicking text selects its style, the
   inspector edits it live in every frame, and Save writes the scale back
   to src/home/type-scale.json (under `vp dev`), which the page imports. */
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type PointerEvent as ReactPointerEvent,
} from "react";
import savedJson from "~/home/type-scale.json";
import {
  FACE_KEYS,
  localFaceCss,
  parseScale,
  phoneFace,
  STYLES,
  typeCss,
  type Face,
  type FontInfo,
  type StyleId,
  type TypeScale,
  type TypeStyle,
} from "~/home/typeScale";
import { fontInfo, withFamily, withGoogle } from "./fonts";
import {
  applyType,
  breakpointOf,
  findOrphans,
  FRAMES,
  install,
  mark,
  setZoom,
  whenHydrated,
  reveal,
  styleSelector,
  type Breakpoint,
  type FrameEvents,
  type FrameId,
} from "./frames";
import { FontPicker, Inspector } from "./Inspector";
import "./design.css";

const SAVED = parseScale(savedJson);
const DRAFT_KEY = "anveio-design:type-draft";
const FRAMES_KEY = "anveio-design:frames";
const GAP = 120;

// Same-ness by value, ignoring key order (phone overrides merge in any order).
const canon = (v: unknown): string =>
  JSON.stringify(v, (_k, x: unknown) =>
    x && typeof x === "object" && !Array.isArray(x)
      ? Object.fromEntries(Object.entries(x).sort(([a], [b]) => (a < b ? -1 : 1)))
      : x,
  );
const same = (a: unknown, b: unknown) => canon(a) === canon(b);

function read<T>(key: string, parse: (v: unknown) => T, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw ? parse(JSON.parse(raw)) : fallback;
  } catch {
    return fallback;
  }
}
function write(key: string, value: unknown) {
  try {
    if (value === undefined) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Private windows and blocked storage just don't keep the draft.
  }
}
const parseFrames = (v: unknown): FrameId[] =>
  Array.isArray(v) ? FRAMES.filter((f) => v.includes(f.id)).map((f) => f.id) : [];

/** A phone override equal to its desktop value is no override. */
function tidy(style: TypeStyle): TypeStyle {
  const phone: Partial<Face> = { ...style.phone };
  for (const k of FACE_KEYS) if (k in phone && same(phone[k], style.desktop[k])) delete phone[k];
  return { desktop: style.desktop, phone };
}

function patchStyle(
  scale: TypeScale,
  id: StyleId,
  bp: Breakpoint,
  patch: Partial<Face>,
): TypeScale {
  const style = scale.styles[id];
  const next =
    bp === "desktop"
      ? { desktop: { ...style.desktop, ...patch }, phone: style.phone }
      : { desktop: style.desktop, phone: { ...style.phone, ...patch } };
  return { ...scale, styles: { ...scale.styles, [id]: tidy(next) } };
}

type History = {
  past: TypeScale[];
  present: TypeScale;
  future: TypeScale[];
  tag: string;
  at: number;
};
type Status =
  | { kind: "idle" }
  | { kind: "saving" }
  | { kind: "saved" }
  | { kind: "copied"; what: string }
  | { kind: "error"; text: string };

const summary = (f: Face) =>
  `${f.family} ${f.weight}${f.italic ? " italic" : ""} · ${+f.size.toFixed(2)}/${+f.lineHeight.toFixed(2)}`;

export function Editor() {
  const [saved, setSaved] = useState(SAVED);
  const [hist, setHist] = useState<History>(() => ({
    past: [],
    present: read(DRAFT_KEY, parseScale, SAVED),
    future: [],
    tag: "",
    at: 0,
  }));
  const draft = hist.present;
  const [selected, setSelected] = useState<StyleId | null>(null);
  const [hovered, setHovered] = useState<StyleId | null>(null);
  const [bp, setBp] = useState<Breakpoint>("desktop");
  const [shown, setShown] = useState<FrameId[]>(() => {
    const stored = read(FRAMES_KEY, parseFrames, []);
    return stored.length ? stored : ["desktop", "iphone17pm", "iphone16e"];
  });
  const [view, setView] = useState({ x: 0, y: 0, k: 0.4 });
  const [space, setSpace] = useState(false);
  const [panning, setPanning] = useState(false);
  const [orphans, setOrphans] = useState<Partial<Record<FrameId, Element[]>>>({});
  const [status, setStatus] = useState<Status>({ kind: "idle" });
  const [replacing, setReplacing] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(0);

  const canvas = useRef<HTMLDivElement>(null);
  const world = useRef<HTMLDivElement>(null);
  const docs = useRef(new Map<FrameId, Document>());
  const frames = useMemo(() => FRAMES.filter((f) => shown.includes(f.id)), [shown]);
  const css = useMemo(() => typeCss(draft), [draft]);
  const cssRef = useRef(css);
  cssRef.current = css;
  const draftRef = useRef(draft);
  draftRef.current = draft;

  const dirty = typeCss(saved) !== css;
  const changed = (id: StyleId) => !same(draft.styles[id], saved.styles[id]);

  useEffect(() => write(DRAFT_KEY, dirty ? draft : undefined), [draft, dirty]);
  useEffect(() => write(FRAMES_KEY, shown), [shown]);

  // Editing
  const edit = useCallback((tag: string, fn: (s: TypeScale) => TypeScale) => {
    setHist((h) => {
      const next = withGoogle(fn(h.present));
      if (same(next, h.present)) return h;
      const now = performance.now();
      const merge = tag !== "" && tag === h.tag && now - h.at < 800;
      return {
        past: merge ? h.past : [...h.past.slice(-199), h.present],
        present: next,
        future: [],
        tag,
        at: now,
      };
    });
  }, []);
  const undo = useCallback(
    () =>
      setHist((h) => {
        const prev = h.past[h.past.length - 1];
        return prev
          ? {
              past: h.past.slice(0, -1),
              present: prev,
              future: [h.present, ...h.future],
              tag: "",
              at: 0,
            }
          : h;
      }),
    [],
  );
  const redo = useCallback(
    () =>
      setHist((h) => {
        const [next, ...future] = h.future;
        return next ? { past: [...h.past, h.present], present: next, future, tag: "", at: 0 } : h;
      }),
    [],
  );

  const pickFamily = (id: StyleId, font: FontInfo) =>
    edit(`family.${id}`, (s) => {
      const style = s.styles[id];
      const face = bp === "desktop" ? style.desktop : phoneFace(style);
      return patchStyle(s, id, bp, withFamily(face, font));
    });

  /** Swaps one family for another in every style and breakpoint. */
  const replaceFamily = (from: string, font: FontInfo) =>
    edit("", (s) => {
      const styles = { ...s.styles };
      for (const { id } of STYLES) {
        const style = styles[id];
        const desktop =
          style.desktop.family === from
            ? { ...style.desktop, ...withFamily(style.desktop, font) }
            : style.desktop;
        const phone =
          style.phone.family === from ||
          (style.phone.family === undefined && style.desktop.family === from)
            ? { ...style.phone, ...withFamily(phoneFace(style), font) }
            : style.phone;
        styles[id] = tidy({ desktop, phone });
      }
      return { ...s, styles };
    });

  // Saving
  const save = useCallback(async () => {
    setStatus({ kind: "saving" });
    const scale = draftRef.current;
    try {
      const r = await fetch("/__design/type-scale", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify(scale),
      });
      if (r.status === 204) {
        setSaved(scale);
        setStatus({ kind: "saved" });
      } else if (r.status === 422) {
        setStatus({ kind: "error", text: await r.text() });
      } else {
        setStatus({
          kind: "error",
          text: "Saving needs the dev server (vp dev). Copy the JSON instead.",
        });
      }
    } catch {
      setStatus({ kind: "error", text: "Couldn't reach the dev server." });
    }
  }, []);
  const copy = async (what: string, text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      setStatus({ kind: "copied", what });
    } catch {
      setStatus({ kind: "error", text: "The clipboard isn't available here." });
    }
  };
  useEffect(() => {
    if (status.kind !== "saved" && status.kind !== "copied") return;
    const t = setTimeout(() => setStatus({ kind: "idle" }), 2000);
    return () => clearTimeout(t);
  }, [status]);

  // Canvas: pan by dragging empty canvas or with space held; zoom with
  // pinch or ⌘/ctrl + wheel, about the pointer.
  const zoomAt = useCallback((cx: number, cy: number, factor: number) => {
    const r = canvas.current?.getBoundingClientRect();
    if (!r) return;
    setView((v) => {
      const k = Math.min(2, Math.max(0.05, v.k * factor));
      const px = cx - r.left;
      const py = cy - r.top;
      return { k, x: px - ((px - v.x) * k) / v.k, y: py - ((py - v.y) * k) / v.k };
    });
  }, []);
  const fit = useCallback(() => {
    const r = canvas.current?.getBoundingClientRect();
    if (!r || frames.length === 0) return;
    const w = frames.reduce((sum, f) => sum + f.width, 0) + GAP * (frames.length - 1);
    const h = Math.max(...frames.map((f) => f.height));
    const k = Math.min((r.width - 96) / w, (r.height - 112) / h, 1);
    setView({ k, x: (r.width - w * k) / 2, y: 64 });
  }, [frames]);
  useEffect(fit, [fit]);

  useEffect(() => {
    const el = canvas.current;
    if (!el) return;
    const wheel = (e: WheelEvent) => {
      e.preventDefault();
      if (e.ctrlKey || e.metaKey) zoomAt(e.clientX, e.clientY, Math.exp(-e.deltaY * 0.01));
      else setView((v) => ({ ...v, x: v.x - e.deltaX, y: v.y - e.deltaY }));
    };
    el.addEventListener("wheel", wheel, { passive: false });
    return () => el.removeEventListener("wheel", wheel);
  }, [zoomAt]);

  const zoomBy = (factor: number) => {
    const r = canvas.current?.getBoundingClientRect();
    if (r) zoomAt(r.left + r.width / 2, r.top + r.height / 2, factor);
  };

  const startPan = (e: ReactPointerEvent<HTMLElement>) => {
    const bare = e.target === e.currentTarget || e.target === world.current;
    if (e.button !== 1 && !space && !bare) return;
    if (e.button === 0 && !space) setSelected(null);
    const start = { x: e.clientX, y: e.clientY, vx: view.x, vy: view.y };
    const el = e.currentTarget;
    el.setPointerCapture(e.pointerId);
    setPanning(true);
    const move = (m: PointerEvent) =>
      setView((v) => ({
        ...v,
        x: start.vx + m.clientX - start.x,
        y: start.vy + m.clientY - start.y,
      }));
    const up = () => {
      setPanning(false);
      el.removeEventListener("pointermove", move);
      el.removeEventListener("pointerup", up);
    };
    el.addEventListener("pointermove", move);
    el.addEventListener("pointerup", up);
  };

  // Keyboard: ⌘Z / ⇧⌘Z, ⌘S, Esc, ⇧1 to fit, ⌘+ / ⌘− to zoom, space to pan.
  const onKey = useCallback(
    (e: KeyboardEvent) => {
      const typing = e.target instanceof HTMLInputElement || e.target instanceof HTMLSelectElement;
      const mod = e.metaKey || e.ctrlKey;
      const key = e.key.toLowerCase();
      if (mod && key === "s") {
        e.preventDefault();
        void save();
      } else if (typing) {
        return;
      } else if (mod && key === "z") {
        e.preventDefault();
        if (e.shiftKey) redo();
        else undo();
      } else if (mod && (key === "=" || key === "+" || key === "-")) {
        e.preventDefault();
        const r = canvas.current?.getBoundingClientRect();
        if (r) zoomAt(r.left + r.width / 2, r.top + r.height / 2, key === "-" ? 0.8 : 1.25);
      } else if (e.shiftKey && (key === "!" || key === "1")) {
        fit();
      } else if (key === "escape") {
        setSelected(null);
      } else if (key === " ") {
        e.preventDefault();
        setSpace(true);
      }
    },
    [fit, redo, save, undo, zoomAt],
  );
  const onKeyUp = useCallback((e: KeyboardEvent) => {
    if (e.key === " ") setSpace(false);
  }, []);
  useEffect(() => {
    window.addEventListener("keydown", onKey);
    window.addEventListener("keyup", onKeyUp);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("keyup", onKeyUp);
    };
  }, [onKey, onKeyUp]);

  // Frames
  const checkTimer = useRef(0);
  const checkOrphans = useCallback(() => {
    clearTimeout(checkTimer.current);
    checkTimer.current = window.setTimeout(async () => {
      const entries = [...docs.current];
      await Promise.all(entries.map(([, d]) => d.fonts.ready));
      requestAnimationFrame(() => {
        const out: Partial<Record<FrameId, Element[]>> = {};
        for (const [id, doc] of entries) {
          for (const el of doc.querySelectorAll("[data-dz-orphan]"))
            el.removeAttribute("data-dz-orphan");
          const found = findOrphans(doc);
          for (const el of found) el.setAttribute("data-dz-orphan", "");
          out[id] = found;
        }
        setOrphans(out);
      });
    }, 150);
  }, []);

  const frameBp = useRef(new Map<FrameId, Breakpoint>());
  const events = useRef<(id: FrameId) => FrameEvents>(() => ({
    select: () => {},
    hover: () => {},
    zoom: () => {},
    key: () => {},
    keyUp: () => {},
    edited: () => {},
  }));
  events.current = (id) => ({
    select: (style) => {
      setSelected(style);
      if (style) setBp(frameBp.current.get(id) ?? "desktop");
    },
    hover: setHovered,
    zoom: (x, y, dy) => zoomAt(x, y, Math.exp(-dy * 0.01)),
    key: onKey,
    keyUp: onKeyUp,
    edited: checkOrphans,
  });

  const onFrameLoad = (id: FrameId, bpOfFrame: Breakpoint, el: HTMLIFrameElement) =>
    whenHydrated(el, () => {
      const doc = el.contentDocument;
      if (!doc) return;
      docs.current.set(id, doc);
      frameBp.current.set(id, bpOfFrame);
      install(
        el,
        () => events.current(id),
        () => cssRef.current,
      );
      setLoaded((n) => n + 1);
    });
  useEffect(() => {
    for (const id of docs.current.keys()) if (!shown.includes(id)) docs.current.delete(id);
  }, [shown]);

  useEffect(() => {
    for (const doc of docs.current.values()) applyType(doc, css, draftRef.current.google);
    checkOrphans();
  }, [css, loaded, checkOrphans]);
  useEffect(() => {
    for (const doc of docs.current.values()) mark(doc, "data-dz-sel", selected);
  }, [selected, loaded]);
  useEffect(() => {
    for (const doc of docs.current.values()) mark(doc, "data-dz-hover", hovered);
  }, [hovered, loaded]);
  useEffect(() => {
    for (const doc of docs.current.values()) setZoom(doc, view.k);
  }, [view.k, loaded]);

  // The editor's own chrome previews local faces app.css doesn't declare.
  useEffect(() => {
    const style = document.createElement("style");
    style.textContent = localFaceCss();
    document.head.append(style);
    return () => style.remove();
  }, []);

  const selectFromList = (id: StyleId) => {
    setSelected(id);
    for (const doc of docs.current.values()) reveal(doc, id);
  };
  const showOrphan = (id: FrameId) => {
    const doc = docs.current.get(id);
    const el = orphans[id]?.[0];
    const hit =
      el && STYLES.find((s) => el.matches(styleSelector(s.id)) || el.closest(styleSelector(s.id)));
    if (!doc || !el || !hit) return;
    setSelected(hit.id);
    setBp(breakpointOf(FRAMES.find((f) => f.id === id) ?? FRAMES[0]));
    const win = doc.defaultView;
    if (win) {
      const top = el.getBoundingClientRect().top + win.scrollY - win.innerHeight / 3;
      win.scrollTo({ top: Math.max(0, top), behavior: "smooth" });
    }
  };

  const families = useMemo(() => {
    const count = new Map<string, number>();
    for (const { id } of STYLES) {
      const style = draft.styles[id];
      for (const f of new Set([style.desktop.family, phoneFace(style).family])) {
        count.set(f, (count.get(f) ?? 0) + 1);
      }
    }
    return [...count].sort((a, b) => b[1] - a[1]);
  }, [draft]);

  const groups = [...new Set(STYLES.map((s) => s.group))];
  const totalOrphans = Object.values(orphans).reduce((n, list) => n + (list?.length ?? 0), 0);
  let x = 0;
  const placed = frames.map((f) => {
    const at = x;
    x += f.width + GAP;
    return { f, at };
  });

  return (
    <div className={`dz ${space ? "dz-space" : ""} ${panning ? "dz-panning" : ""}`}>
      <header className="dz-bar">
        <div className="dz-title">
          <strong>Anveio</strong>
          <span>/</span>
          Home · Type
          {dirty && <span className="dz-dirty" title="Unsaved changes" />}
        </div>
        <div className="dz-frames" role="group" aria-label="Frames">
          {FRAMES.map((f) => (
            <button
              key={f.id}
              aria-pressed={shown.includes(f.id)}
              onClick={() =>
                setShown((s) =>
                  s.includes(f.id)
                    ? s.filter((v) => v !== f.id)
                    : FRAMES.filter((g) => g.id === f.id || s.includes(g.id)).map((g) => g.id),
                )
              }
            >
              {f.label}
            </button>
          ))}
        </div>
        <div className="dz-actions">
          <span className={`dz-orphan-total ${totalOrphans ? "bad" : ""}`}>
            {totalOrphans === 0
              ? "No orphans"
              : `${totalOrphans} orphan${totalOrphans === 1 ? "" : "s"}`}
          </span>
          <div className="dz-zoom">
            <button title="Zoom out (⌘−)" onClick={() => zoomBy(0.8)}>
              −
            </button>
            <button title="Fit (⇧1)" onClick={fit}>
              {Math.round(view.k * 100)}%
            </button>
            <button title="Zoom in (⌘+)" onClick={() => zoomBy(1.25)}>
              +
            </button>
          </div>
          <button
            className="dz-icon"
            title="Undo (⌘Z)"
            disabled={hist.past.length === 0}
            onClick={undo}
          >
            <UndoIcon />
          </button>
          <button
            className="dz-icon"
            title="Redo (⇧⌘Z)"
            disabled={hist.future.length === 0}
            onClick={redo}
          >
            <UndoIcon flip />
          </button>
          <button className="dz-btn" onClick={() => copy("CSS", css)}>
            Copy CSS
          </button>
          <button
            className="dz-btn"
            onClick={() => copy("JSON", `${JSON.stringify(draft, null, 2)}\n`)}
          >
            Copy JSON
          </button>
          <button
            className="dz-btn"
            disabled={!dirty}
            title="Discard the draft and return to the saved scale"
            onClick={() => edit("", () => saved)}
          >
            Discard
          </button>
          <button
            className="dz-btn dz-primary"
            disabled={!dirty || status.kind === "saving"}
            onClick={() => void save()}
          >
            {status.kind === "saving" ? "Saving…" : "Save"}
          </button>
        </div>
      </header>

      <aside className="dz-layers">
        <h2>Text styles</h2>
        {groups.map((g) => (
          <section key={g}>
            <h3>{g}</h3>
            {STYLES.filter((s) => s.group === g).map((s) => {
              const style = draft.styles[s.id];
              const face = bp === "desktop" ? style.desktop : phoneFace(style);
              return (
                <button
                  key={s.id}
                  className={`dz-layer ${selected === s.id ? "selected" : ""} ${hovered === s.id ? "hovered" : ""}`}
                  onClick={() => selectFromList(s.id)}
                  onPointerEnter={() => setHovered(s.id)}
                  onPointerLeave={() => setHovered(null)}
                >
                  <span className="dz-layer-name">
                    <TextIcon />
                    {s.label}
                    {changed(s.id) && <span className="dz-dot" title="Changed since save" />}
                    {Object.keys(style.phone).length > 0 && (
                      <span className="dz-phone-mark" title="Has phone overrides">
                        <PhoneIcon />
                      </span>
                    )}
                  </span>
                  <span className="dz-layer-meta">{summary(face)}</span>
                </button>
              );
            })}
          </section>
        ))}
        <h2>Fonts in use</h2>
        <div className="dz-fonts">
          {families.map(([family, n]) => (
            <div key={family} className="dz-font-row">
              <span style={{ fontFamily: `"${family}", var(--dz-ui)` }}>{family}</span>
              <span className="dz-layer-meta">{n}</span>
              <button className="dz-link" onClick={() => setReplacing(family)}>
                Replace
              </button>
              {replacing === family && (
                <FontPicker
                  current={family}
                  scale={draft}
                  onPick={(f) => {
                    replaceFamily(family, f);
                    setReplacing(null);
                  }}
                  onClose={() => setReplacing(null)}
                />
              )}
            </div>
          ))}
        </div>
      </aside>

      <main className="dz-canvas" ref={canvas} onPointerDown={startPan}>
        <div
          className="dz-world"
          ref={world}
          style={{ transform: `translate(${view.x}px, ${view.y}px) scale(${view.k})` }}
        >
          {placed.map(({ f, at }) => {
            const fbp = breakpointOf(f);
            const n = orphans[f.id]?.length ?? 0;
            return (
              <div
                key={f.id}
                className="dz-frame"
                style={{ left: at, width: f.width, height: f.height }}
              >
                <div
                  className="dz-frame-label"
                  style={{ width: f.width * view.k, transform: `scale(${1 / view.k})` }}
                >
                  <button
                    className={`dz-frame-name ${bp === fbp ? "on" : ""}`}
                    onClick={() => setBp(fbp)}
                  >
                    {f.label}{" "}
                    <span>
                      {f.width}×{f.height}
                    </span>
                    <span className="dz-frame-bp">
                      {fbp === "phone" ? "Phone styles" : "Desktop styles"}
                    </span>
                  </button>
                  <button
                    className={`dz-frame-orphans ${n ? "bad" : ""}`}
                    disabled={!n}
                    onClick={() => showOrphan(f.id)}
                  >
                    {n ? `${n} orphan${n === 1 ? "" : "s"}` : "0 orphans"}
                  </button>
                </div>
                <iframe
                  title={`${f.label} frame`}
                  src="/"
                  width={f.width}
                  height={f.height}
                  onLoad={(e) => onFrameLoad(f.id, fbp, e.currentTarget)}
                />
              </div>
            );
          })}
        </div>
        {(space || panning) && <div className="dz-shield" />}
        {status.kind !== "idle" && (
          <div className={`dz-toast ${status.kind === "error" ? "bad" : ""}`}>
            {status.kind === "saving" && "Saving…"}
            {status.kind === "saved" && "Saved to src/home/type-scale.json"}
            {status.kind === "copied" && `${status.what} copied`}
            {status.kind === "error" && status.text}
            {status.kind === "error" && (
              <button className="dz-link" onClick={() => setStatus({ kind: "idle" })}>
                Dismiss
              </button>
            )}
          </div>
        )}
        <div className="dz-hints">
          Click text to select · double-click to edit copy (preview only) · drag or scroll to pan ·
          pinch or ⌘ scroll to zoom
        </div>
      </main>

      <aside className="dz-inspector">
        {selected ? (
          <Inspector
            scale={draft}
            id={selected}
            bp={bp}
            setBp={setBp}
            count={
              [...docs.current.values()][0]?.querySelectorAll(styleSelector(selected)).length ?? 0
            }
            changed={changed(selected)}
            patch={(patch, tag) =>
              edit(`${selected}.${bp}.${tag}`, (s) => patchStyle(s, selected, bp, patch))
            }
            inherit={(keys) =>
              edit("", (s) => {
                const style = s.styles[selected];
                const phone: Partial<Face> = { ...style.phone };
                for (const k of keys) delete phone[k];
                return {
                  ...s,
                  styles: { ...s.styles, [selected]: { desktop: style.desktop, phone } },
                };
              })
            }
            revert={() =>
              edit("", (s) => ({
                ...s,
                styles: { ...s.styles, [selected]: saved.styles[selected] },
              }))
            }
            pickFamily={(f) => pickFamily(selected, f)}
          />
        ) : (
          <div className="dz-inspector-empty">
            <h2>Type</h2>
            <p>Click any text in a frame, or pick a style on the left.</p>
            <p>
              Desktop frames use the desktop values; frames 860px wide and narrower use the phone
              values, which follow desktop unless overridden.
            </p>
            <p>
              Save writes <code>src/home/type-scale.json</code>; the home page picks it up on the
              next reload or hot update. Your draft survives a reload until you save or discard it.
            </p>
            <FontSummary scale={draft} />
          </div>
        )}
      </aside>
    </div>
  );
}

function FontSummary({ scale }: { scale: TypeScale }) {
  const missing = [...new Set(STYLES.map((s) => scale.styles[s.id].desktop.family))].filter(
    (f) => !fontInfo(f, scale),
  );
  if (missing.length === 0) return null;
  return <p className="dz-warn">Unknown families: {missing.join(", ")}</p>;
}

function PhoneIcon() {
  return (
    <svg viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
      <rect x="3.5" y="1.5" width="5" height="9" rx="1" fill="none" stroke="currentColor" />
    </svg>
  );
}

function UndoIcon({ flip }: { flip?: boolean }) {
  return (
    <svg
      viewBox="0 0 16 16"
      width="16"
      height="16"
      aria-hidden="true"
      style={flip ? { transform: "scaleX(-1)" } : undefined}
    >
      <path
        d="M5.5 4L2.5 7l3 3M3 7h6.5a3.5 3.5 0 010 7H8"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.3"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

function TextIcon() {
  return (
    <svg viewBox="0 0 12 12" width="12" height="12" aria-hidden="true">
      <path d="M2 2.5h8M6 2.5v7.5" fill="none" stroke="currentColor" strokeWidth="1.2" />
    </svg>
  );
}
