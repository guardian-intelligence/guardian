import { useEffect } from "react";

// The deck's only browser code. Nothing here may affect layout: the deck is
// sized and laid out entirely by CSS, so the server-rendered page is final.
// Lint (root vite.config.ts) bans browser globals and state/effect hooks
// everywhere else in this app.

// Arrow and page keys step one slide at a time.
export function useKeyboardNav(ids: readonly string[]) {
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA")) return;
      const delta = {
        ArrowDown: 1,
        PageDown: 1,
        ArrowRight: 1,
        ArrowUp: -1,
        PageUp: -1,
        ArrowLeft: -1,
      }[event.key];
      if (!delta) return;
      event.preventDefault();
      const tops = ids.map((id) => document.getElementById(id)?.getBoundingClientRect().top ?? 0);
      const current = tops.reduce(
        (best, top, i) => (Math.abs(top) < Math.abs(tops[best]!) ? i : best),
        0,
      );
      const next = Math.max(0, Math.min(ids.length - 1, current + delta));
      document.getElementById(ids[next]!)?.scrollIntoView({ behavior: "smooth", block: "start" });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [ids]);
}
