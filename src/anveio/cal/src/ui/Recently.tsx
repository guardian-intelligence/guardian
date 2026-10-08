import { useState } from "react";

import { ENTRIES, GROUP_NOUN, type Entry, type EntryKind } from "../content.ts";

const ICONS = {
  event: { d: "M4 6h16v14H4zM4 10h16M9 3v4M15 3v4", fill: "none", stroke: "currentColor" },
  code: {
    d: "M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.1.79-.25.79-.56v-2c-3.2.7-3.87-1.37-3.87-1.37-.52-1.33-1.28-1.69-1.28-1.69-1.04-.71.08-.7.08-.7 1.15.08 1.76 1.18 1.76 1.18 1.03 1.76 2.69 1.25 3.35.96.1-.75.4-1.25.73-1.54-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.29 1.18-3.1-.12-.29-.51-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.18 1.84 1.18 3.1 0 4.42-2.69 5.39-5.26 5.68.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.56A11.5 11.5 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5z",
    fill: "currentColor",
    stroke: "none",
  },
  post: {
    d: "M17.8 3h3.1l-6.8 7.7L22 21h-6.2l-4.9-6.3L5.3 21H2.2l7.3-8.3L2 3h6.4l4.4 5.8z",
    fill: "currentColor",
    stroke: "none",
  },
} as const satisfies Record<EntryKind, { d: string; fill: string; stroke: string }>;

const MONTHS = [
  "Jan",
  "Feb",
  "Mar",
  "Apr",
  "May",
  "Jun",
  "Jul",
  "Aug",
  "Sep",
  "Oct",
  "Nov",
  "Dec",
] as const;
const dateLabel = (iso: string) => {
  const [, m, d] = iso.split("-");
  return `${MONTHS[Number(m) - 1] ?? ""} ${Number(d)}`;
};

type Row =
  | { type: "item"; entry: Entry; sub: boolean }
  | { type: "more"; kind: keyof typeof GROUP_NOUN; text: string; open: boolean };

function Item({ entry, sub, sep }: { entry: Entry; sub: boolean; sep: boolean }) {
  const icon = ICONS[entry.kind];
  return (
    <li>
      <a className={sub ? "row row-sub" : "row"} href={entry.url} target="_blank" rel="noreferrer">
        {!sub && (
          <span className="row-tile">
            <svg
              width="18"
              height="18"
              viewBox="0 0 24 24"
              fill={icon.fill}
              stroke={icon.stroke}
              strokeWidth="2.2"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden="true"
            >
              <path d={icon.d} />
            </svg>
          </span>
        )}
        <span className="row-text">{entry.text}</span>
        <time className="row-date" dateTime={entry.date}>
          {dateLabel(entry.date)}
        </time>
        <svg
          className="row-chevron"
          width="8"
          height="13"
          viewBox="0 0 8 13"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <path d="M1.5 1.5l5 5-5 5" />
        </svg>
        {sep && <span className="sep" />}
      </a>
    </li>
  );
}

export function Recently() {
  const [open, setOpen] = useState<Partial<Record<keyof typeof GROUP_NOUN, boolean>>>({});
  const sorted = ENTRIES.map((e, i) => ({ e, i }))
    .sort((a, b) => (a.e.date < b.e.date ? 1 : a.e.date > b.e.date ? -1 : a.i - b.i))
    .map(({ e }) => e);

  // Grouped kinds collapse to their newest item, then "N more recent …".
  const rows: Row[] = [];
  const placed = new Set<EntryKind>();
  for (const e of sorted) {
    if (e.kind === "event") {
      rows.push({ type: "item", entry: e, sub: false });
      continue;
    }
    if (placed.has(e.kind)) continue;
    placed.add(e.kind);
    const kind = e.kind;
    const items = sorted.filter((x) => x.kind === kind);
    rows.push({ type: "item", entry: e, sub: false });
    const rest = items.slice(1);
    if (rest.length === 0) continue;
    const isOpen = open[kind] ?? false;
    rows.push({
      type: "more",
      kind,
      open: isOpen,
      text: isOpen ? "Show fewer" : `${rest.length} more recent ${GROUP_NOUN[kind]}…`,
    });
    if (isOpen) rest.forEach((x) => rows.push({ type: "item", entry: x, sub: true }));
  }

  return (
    <section>
      <h2 className="section-header t-foot">Recently</h2>
      <ol className="recently">
        {rows.map((r, i) => {
          const sep = i < rows.length - 1 && rows[i + 1]?.type !== "more";
          if (r.type === "item")
            return <Item key={r.entry.url} entry={r.entry} sub={r.sub} sep={sep} />;
          return (
            <li key={`more-${r.kind}`}>
              <button
                type="button"
                className="row-more"
                aria-expanded={r.open}
                onClick={() => setOpen({ ...open, [r.kind]: !r.open })}
              >
                {r.text}
                {sep && <span className="sep" />}
              </button>
            </li>
          );
        })}
      </ol>
    </section>
  );
}
