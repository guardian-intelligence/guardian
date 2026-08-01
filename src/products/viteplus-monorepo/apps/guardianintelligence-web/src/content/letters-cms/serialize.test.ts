import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import * as v from "valibot";
import { LetterFrontmatterSchema } from "../letter-schema";
import { parseLetterFile, serializeLetter } from "./serialize";

// The letters in ./letters/*.md are baked into the site image, and the
// Directus sync rewrites them on every pull. Byte-identical round-tripping
// is what makes a content no-op a build no-op (and a rendering no-op); any
// letter that stops round-tripping means the emitter's style rules no
// longer cover the corpus, and this failure is the prompt to extend them.

const LETTERS_DIR = join(import.meta.dirname, "..", "letters");

const letterFiles = readdirSync(LETTERS_DIR).filter((name) => name.endsWith(".md"));

describe("letters-cms round trip", () => {
  it("finds the letters corpus", () => {
    expect(letterFiles.length).toBeGreaterThan(0);
  });

  it.each(letterFiles)("%s round-trips byte-identically", (name) => {
    const text = readFileSync(join(LETTERS_DIR, name), "utf8");
    const record = parseLetterFile(text);
    expect(serializeLetter(record)).toBe(text);
  });

  it.each(letterFiles)("%s frontmatter satisfies the letter schema", (name) => {
    const text = readFileSync(join(LETTERS_DIR, name), "utf8");
    const { frontmatter } = parseLetterFile(text);
    // Drafts (no summary) are legitimate letter files; the schema treats
    // summary as optional the same way the site loader does.
    const parsed = v.safeParse(LetterFrontmatterSchema, frontmatter);
    expect(parsed.success, JSON.stringify(parsed.issues ?? null)).toBe(true);
    expect(frontmatter.slug).toBe(name.replace(/\.md$/, ""));
  });
});
