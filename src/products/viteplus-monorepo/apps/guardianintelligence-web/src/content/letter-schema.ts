import * as v from "valibot";

// summary is the only frontmatter field allowed to be absent — that absence
// is the publish gate. A letter with no summary is a draft: it parses, but
// is filtered out of LETTERS so it does not show up on /letters or
// /letters/$slug. Authors can leave a stub while drafting without breaking
// the build, and ship by filling in the summary.
export const LetterFrontmatterSchema = v.pipe(
  v.object({
    slug: v.pipe(v.string(), v.minLength(1)),
    title: v.pipe(v.string(), v.minLength(1)),
    // YYYY-MM-DD only. The Vite plugin coerces YAML dates to this shape, so
    // anything else here is an authoring mistake worth surfacing.
    publishedAt: v.pipe(v.string(), v.regex(/^\d{4}-\d{2}-\d{2}$/)),
    flare: v.pipe(v.string(), v.minLength(1)),
    // dispatch: a letter from the author to a younger self — a titled
    // headline, signed by the author. correspondence: a letter received from
    // someone else — it opens with a salutation ("Dear X,") and carries the
    // sender's own sign-off in the body. Required so each letter declares its
    // nature rather than inheriting a silent default.
    kind: v.picklist(["dispatch", "correspondence"]),
    summary: v.optional(v.string(), ""),
    // Machine-readable provenance, never rendered. A letter may be written to
    // be open-ended on the page — a correspondence whose sender the reader is
    // left to imagine — while still owing the record an account of what it is
    // and who wrote it. `author` names the real author (the page may say
    // otherwise or nothing at all) and `authorTitle` disambiguates them;
    // `description` is the one-line account of the work; `note` is the
    // author's own longer statement of context. All are carried as JSON-LD on
    // /letters/$slug for crawlers, archives, and search — readers never see
    // them.
    author: v.optional(v.string(), ""),
    authorTitle: v.optional(v.string(), ""),
    description: v.optional(v.string(), ""),
    note: v.optional(v.string(), ""),
  }),
  v.check(
    (fm) => fm.title.includes(fm.flare),
    "flare must be a substring of title — the OG card highlights it",
  ),
);
