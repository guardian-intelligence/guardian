import * as v from "valibot";
import { LetterFrontmatterSchema } from "./letter-schema";

// Letters — Guardian's long-form. One .md file per letter under
// ./letters/*.md. Frontmatter declares the metadata (see ./letter-schema.ts);
// the body is plain markdown.
//
// The files are authored in Directus and mirrored here by the letters-cms
// sync (./letters-cms/cli.ts): edit in the Studio, pull, open a PR. The Vite
// plugin (vite.config: company:letters-markdown) parses each file at build
// time; the browser only ever sees pre-rendered HTML, and the public site
// never talks to Directus.

const LetterModuleSchema = v.object({
  default: v.object({
    frontmatter: LetterFrontmatterSchema,
    html: v.string(),
    leadHtml: v.string(),
    continuationHtml: v.string(),
  }),
});

export type Letter = v.InferOutput<typeof LetterFrontmatterSchema> & {
  readonly bodyHtml: string;
  readonly leadHtml: string;
  readonly continuationHtml: string;
};

export const LETTERS_META = {
  title: "Letters — Guardian",
  description:
    "Long-form from Guardian. Published when we have something to say, not on a calendar.",
  editor: "Guardian",
  siteURL: "https://guardianintelligence.org",
} as const;

function parseLetter(path: string, mod: unknown): Letter {
  const result = v.safeParse(LetterModuleSchema, mod);
  if (!result.success) {
    const issues = result.issues
      .map((i) => `  - ${i.path?.map((p) => String(p.key)).join(".") ?? "<root>"}: ${i.message}`)
      .join("\n");
    throw new Error(`letters: ${path} frontmatter is invalid:\n${issues}`);
  }
  return {
    ...result.output.default.frontmatter,
    bodyHtml: result.output.default.html,
    leadHtml: result.output.default.leadHtml,
    continuationHtml: result.output.default.continuationHtml,
  };
}

const RAW_LETTERS = import.meta.glob<unknown>("./letters/*.md", { eager: true });

export const LETTERS: readonly Letter[] = Object.entries(RAW_LETTERS)
  .map(([path, mod]) => parseLetter(path, mod))
  .filter((letter) => letter.summary.trim() !== "");

export function letterBySlug(slug: string): Letter | undefined {
  return LETTERS.find((letter) => letter.slug === slug);
}

export function sortedLetters(): readonly Letter[] {
  return [...LETTERS].sort((a, b) => (a.publishedAt < b.publishedAt ? 1 : -1));
}
