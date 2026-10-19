// The canvas's frames: the real home page at device widths. These helpers
// reach into each frame's document (same origin) to swap its type CSS, mark
// selections and find orphans.
import { googleHref, PHONE_MAX, STYLES, type StyleId, type TypeScale } from "~/home/typeScale";

export const FRAMES = [
  { id: "desktop", label: "Desktop", width: 1440, height: 900 },
  { id: "laptop", label: "Laptop", width: 1280, height: 800 },
  { id: "ipad", label: "iPad Air", width: 820, height: 1180 },
  { id: "iphone17pm", label: "iPhone 17 Pro Max", width: 440, height: 956 },
  { id: "iphone16e", label: "iPhone 16e", width: 390, height: 844 },
  { id: "iphoneSE", label: "iPhone SE", width: 375, height: 667 },
  { id: "narrow", label: "320", width: 320, height: 640 },
] as const;
export type Frame = (typeof FRAMES)[number];
export type FrameId = Frame["id"];
export type Breakpoint = "desktop" | "phone";

export const breakpointOf = (f: Frame): Breakpoint => (f.width <= PHONE_MAX ? "phone" : "desktop");

export const styleSelector = (id: StyleId) =>
  `.hp :is(${STYLES.find((s) => s.id === id)?.selector ?? "never"})`;

const isElement = (n: unknown): n is Element =>
  typeof n === "object" && n !== null && "nodeType" in n && n.nodeType === 1;

/** The style of the innermost styled element at or around `node`. */
export function styleAt(node: unknown): { id: StyleId; el: Element } | undefined {
  if (!isElement(node)) return undefined;
  let best: { id: StyleId; el: Element; depth: number } | undefined;
  for (const s of STYLES) {
    const el = node.closest(styleSelector(s.id));
    if (!el) continue;
    let depth = 0;
    for (let p = el.parentElement; p; p = p.parentElement) depth++;
    if (!best || depth > best.depth) best = { id: s.id, el, depth };
  }
  return best && { id: best.id, el: best.el };
}

// Selection chrome inside the frame, kept a constant screen width at any
// zoom by --dz-k (1 / zoom).
const CHROME = `
[data-dz-hover] { outline: calc(1px * var(--dz-k, 1)) solid #0d99ff !important; outline-offset: calc(2px * var(--dz-k, 1)); }
[data-dz-sel] { outline: calc(2px * var(--dz-k, 1)) solid #0d99ff !important; outline-offset: calc(2px * var(--dz-k, 1)); }
[data-dz-orphan]:not([data-dz-sel]) { outline: calc(1.5px * var(--dz-k, 1)) dashed #f24822 !important; outline-offset: calc(2px * var(--dz-k, 1)); }
[contenteditable] { outline: calc(2px * var(--dz-k, 1)) solid #9747ff !important; cursor: text !important; }
.hp, .hp * { cursor: default !important; }
`;

export type FrameEvents = {
  select: (id: StyleId | null) => void;
  hover: (id: StyleId | null) => void;
  zoom: (clientX: number, clientY: number, deltaY: number) => void;
  key: (e: KeyboardEvent) => void;
  keyUp: (e: KeyboardEvent) => void;
  edited: () => void;
};

/** Calls `then` once React has hydrated the frame's page (its fiber is on
    the footer, the last thing rendered), so edits never race hydration. */
export function whenHydrated(frame: HTMLIFrameElement, then: () => void) {
  const started = performance.now();
  const poll = () => {
    const last = frame.contentDocument?.querySelector(".hp-foot");
    if (last && Object.keys(last).some((k) => k.startsWith("__reactFiber"))) then();
    else if (performance.now() - started < 15000) setTimeout(poll, 50);
  };
  poll();
}

/** Wires a hydrated frame: chrome, selection by click, text edits on double click. */
export function install(
  frame: HTMLIFrameElement,
  events: () => FrameEvents,
  desiredCss: () => string,
) {
  const doc = frame.contentDocument;
  if (!doc || doc.getElementById("dz-chrome")) return;
  const chrome = doc.createElement("style");
  chrome.id = "dz-chrome";
  chrome.textContent = CHROME;
  const zoom = doc.createElement("style");
  zoom.id = "dz-zoom";
  doc.head.append(chrome, zoom);
  // Open every answer, so FAQ answers can be seen and selected.
  for (const d of doc.querySelectorAll<HTMLDetailsElement>(".hp details")) d.open = true;

  const editing = (t: unknown) => isElement(t) && t.closest("[contenteditable]") !== null;
  doc.addEventListener(
    "click",
    (e) => {
      if (editing(e.target)) return;
      e.preventDefault();
      e.stopPropagation();
      events().select(styleAt(e.target)?.id ?? null);
    },
    true,
  );
  const HTMLElement = doc.defaultView?.HTMLElement;
  doc.addEventListener("dblclick", (e) => {
    const el = styleAt(e.target)?.el;
    if (!HTMLElement || !(el instanceof HTMLElement)) return;
    el.contentEditable = "plaintext-only";
    el.focus();
    el.addEventListener("input", () => events().edited());
    el.addEventListener(
      "blur",
      () => {
        el.removeAttribute("contenteditable");
        events().edited();
      },
      { once: true },
    );
  });
  doc.addEventListener("mouseover", (e) => events().hover(styleAt(e.target)?.id ?? null));
  doc.addEventListener("mouseleave", () => events().hover(null));
  doc.addEventListener(
    "wheel",
    (e) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      const r = frame.getBoundingClientRect();
      const k = r.width / frame.offsetWidth;
      events().zoom(r.left + e.clientX * k, r.top + e.clientY * k, e.deltaY);
    },
    { passive: false },
  );
  doc.addEventListener("keydown", (e) => {
    if (editing(e.target)) {
      if (e.key === "Escape" && HTMLElement && e.target instanceof HTMLElement) e.target.blur();
      return;
    }
    if (e.key === " ") e.preventDefault();
    events().key(e);
  });
  doc.addEventListener("keyup", (e) => events().keyUp(e));

  // React re-renders the page's own <style> on hot reloads; keep the draft.
  new MutationObserver(() => {
    const style = doc.getElementById("hp-type");
    if (style && style.textContent !== desiredCss()) style.textContent = desiredCss();
  }).observe(doc.body, { childList: true, subtree: true, characterData: true });
}

export function applyType(doc: Document, css: string, google: TypeScale["google"]) {
  const style = doc.getElementById("hp-type");
  if (style && style.textContent !== css) style.textContent = css;
  for (const [family, font] of Object.entries(google)) {
    if (doc.head.querySelector(`link[data-dz-font="${CSS.escape(family)}"]`)) continue;
    const link = doc.createElement("link");
    link.rel = "stylesheet";
    link.href = googleHref([[family, font]]);
    link.dataset["dzFont"] = family;
    doc.head.append(link);
  }
}

/** Keeps selection outlines a constant width on screen at zoom `k`. */
export function setZoom(doc: Document, k: number) {
  const style = doc.getElementById("dz-zoom");
  if (style) style.textContent = `:root { --dz-k: ${1 / k}; }`;
}

export function mark(doc: Document, attr: string, id: StyleId | null) {
  for (const el of doc.querySelectorAll(`[${attr}]`)) el.removeAttribute(attr);
  if (id) for (const el of doc.querySelectorAll(styleSelector(id))) el.setAttribute(attr, "");
}

/** Scrolls the frame so the style's first element sits a third of the way down. */
export function reveal(doc: Document, id: StyleId) {
  const win = doc.defaultView;
  const el = doc.querySelector(styleSelector(id));
  if (!win || !el) return;
  const top = el.getBoundingClientRect().top + win.scrollY - win.innerHeight / 3;
  win.scrollTo({ top: Math.max(0, top), behavior: "smooth" });
}

// Blocks checked for orphans: every styled text, plus the band's per-line
// spans, which wrap on their own.
const BLOCKS = [...STYLES.map((s) => styleSelector(s.id)), ".hp-infra p span"].join(", ");

/** Text blocks whose last line holds a single word. */
export function findOrphans(doc: Document): Element[] {
  const out: Element[] = [];
  for (const el of doc.querySelectorAll(BLOCKS)) {
    if (el.closest("[aria-hidden=true]")) continue;
    const tops: number[] = [];
    const walker = doc.createTreeWalker(el, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n; n = walker.nextNode()) {
      const text = n.nodeValue ?? "";
      for (const m of text.matchAll(/\S+/g)) {
        const r = doc.createRange();
        r.setStart(n, m.index);
        r.setEnd(n, m.index + m[0].length);
        // A word split at a hyphen belongs to the line where it ends.
        const rects = r.getClientRects();
        const rect = rects[rects.length - 1];
        if (rect) tops.push(Math.round(rect.top));
      }
    }
    if (tops.length < 2) continue;
    const last = tops[tops.length - 1] ?? 0;
    const onLast = tops.filter((t) => Math.abs(t - last) < 4).length;
    if (onLast === 1 && tops.length > 1) out.push(el);
  }
  return out;
}
