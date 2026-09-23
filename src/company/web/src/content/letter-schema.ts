import * as v from "valibot";

// status is the publish gate, Directus's standard draft/published field: a
// draft stays in Directus, invisible to the anonymous role and to /letters,
// until it is flipped to published in the Studio.
export const LetterFrontmatterSchema = v.pipe(
  v.object({
    slug: v.pipe(v.string(), v.minLength(1)),
    title: v.pipe(v.string(), v.minLength(1)),
    // YYYY-MM-DD only. Directus date fields serialize to this shape over the
    // REST API, so anything else here is an authoring mistake worth surfacing.
    publishedAt: v.pipe(v.string(), v.regex(/^\d{4}-\d{2}-\d{2}$/)),
    flare: v.pipe(v.string(), v.minLength(1)),
    // dispatch: a letter from the author to a younger self — a titled
    // headline, signed by the author. correspondence: a letter received from
    // someone else — it opens with a salutation ("Dear X,") and carries the
    // sender's own sign-off in the body. Required so each letter declares its
    // nature rather than inheriting a silent default.
    kind: v.picklist(["dispatch", "correspondence"]),
    status: v.picklist(["draft", "published"]),
    // Machine-readable provenance, never rendered. A letter may be written to
    // be open-ended on the page — a correspondence whose sender the reader is
    // left to imagine — while still owing the record an account of what it is
    // and who wrote it. `author` names the real author (the page may say
    // otherwise or nothing at all) and `authorTitle` disambiguates them;
    // `description` is the one-line account of the work and the page's
    // meta/OG description (absent, the letter's opening words stand in);
    // `note` is the author's own longer statement of context. All are carried
    // as JSON-LD on /letters/$slug for crawlers, archives, and search —
    // readers never see them.
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
