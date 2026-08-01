import matter from "gray-matter";

// Letter file <-> record conversion for the Directus authoring sync.
//
// The public site bakes ./letters/*.md at image-build time, so the sync's
// contract is byte fidelity: serialize(parse(file)) must reproduce the file
// exactly, or a no-op Directus round trip would dirty git and re-render
// letters HTML. The emitter therefore commits to one YAML style per key
// (the style the letters already use) instead of delegating to a YAML
// dumper with its own opinions, and self-verifies every emission by
// re-parsing it. A letter that needs a style this file cannot reproduce
// fails loudly at serialize time and in serialize.test.ts — extend the
// rules consciously, never silently.

export const FRONTMATTER_KEYS = [
  "slug",
  "title",
  "publishedAt",
  "flare",
  "kind",
  "summary",
  "author",
  "authorTitle",
  "description",
  "note",
] as const;

export type FrontmatterKey = (typeof FRONTMATTER_KEYS)[number];

// Keys emitted as double-quoted scalars; the rest are plain, except the
// folded (>-) prose keys below.
const QUOTED_KEYS: ReadonlySet<FrontmatterKey> = new Set(["summary"]);
const FOLDED_KEYS: ReadonlySet<FrontmatterKey> = new Set(["description", "note"]);

export interface LetterRecord {
  readonly frontmatter: Partial<Record<FrontmatterKey, string>>;
  // Everything after the closing frontmatter fence, verbatim — including
  // the blank line that separates fence from prose.
  readonly body: string;
}

function asDateString(value: unknown): string | undefined {
  if (value instanceof Date) return value.toISOString().slice(0, 10);
  if (typeof value === "string") return value;
  return undefined;
}

export function parseLetterFile(text: string): LetterRecord {
  const { data, content } = matter(text);
  const frontmatter: Partial<Record<FrontmatterKey, string>> = {};
  for (const key of Object.keys(data)) {
    if (!(FRONTMATTER_KEYS as readonly string[]).includes(key)) {
      throw new Error(`letters-cms: unknown frontmatter key "${key}"`);
    }
    const value = key === "publishedAt" ? asDateString(data[key]) : data[key];
    if (typeof value !== "string") {
      throw new Error(`letters-cms: frontmatter key "${key}" is not a string`);
    }
    frontmatter[key as FrontmatterKey] = value;
  }
  return { frontmatter, body: content };
}

const PLAIN_SAFE = /^[^\s"'>|#&*!?%@`[\]{},:-][^#\n]*$/;

function emit(key: FrontmatterKey, value: string): string {
  if (QUOTED_KEYS.has(key)) return `${key}: ${JSON.stringify(value)}`;
  if (FOLDED_KEYS.has(key)) {
    if (value.includes("\n")) {
      throw new Error(`letters-cms: multi-line "${key}" has no committed style yet`);
    }
    return `${key}: >-\n  ${value}`;
  }
  if (
    !PLAIN_SAFE.test(value) ||
    value.includes(": ") ||
    value.endsWith(":") ||
    value.trim() !== value
  ) {
    throw new Error(
      `letters-cms: "${key}: ${value}" cannot be a plain YAML scalar; commit a style for it`,
    );
  }
  return `${key}: ${value}`;
}

export function serializeLetter(record: LetterRecord): string {
  const lines: string[] = [];
  for (const key of FRONTMATTER_KEYS) {
    const value = record.frontmatter[key];
    if (value === undefined) continue;
    lines.push(emit(key, value));
  }
  const text = `---\n${lines.join("\n")}\n---\n${record.body}`;

  // Self-check: the emitted bytes must parse back to the same record, so a
  // style drift can never silently corrupt a letter.
  const reparsed = parseLetterFile(text);
  const same =
    reparsed.body === record.body &&
    FRONTMATTER_KEYS.every((key) => reparsed.frontmatter[key] === record.frontmatter[key]);
  if (!same) {
    throw new Error(
      `letters-cms: serialized form of "${record.frontmatter.slug ?? "<unknown>"}" did not survive re-parsing`,
    );
  }
  return text;
}
