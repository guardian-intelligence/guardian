import type * as v from "valibot";
import type { LetterFrontmatterSchema } from "./letter-schema";

// Letters — Guardian's long-form. Authored in Directus (the Studio at
// cms.guardianintelligence.org) and served from it at request time by
// ./letters.server.ts. This module is the client-safe surface: the Letter
// shape and the section metadata. Letter data only ever reaches the client
// through route loaders.

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
