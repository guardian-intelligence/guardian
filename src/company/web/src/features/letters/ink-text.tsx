import { Fragment } from "react";
import { inkSpanClasses } from "~/features/letters/ink";

// Render-time twin of ink.ts's inkWrapHtml, for text the server renders as
// React rather than pre-built HTML (the index excerpt). Words count from 0 in
// the same document order as the lead paragraph they preview, so a word wears
// the same ink on the index as it does once the letter is opened. Both sides
// are pure functions of (slug, index): SSR and hydration agree by construction.
export function InkText({ slug, text }: { readonly slug: string; readonly text: string }) {
  let wordIndex = 0;
  return (
    <>
      {text.split(/(\s+)/).map((token, i) =>
        token === "" || /^\s+$/.test(token) ? (
          <Fragment key={i}>{token}</Fragment>
        ) : (
          <span key={i} className={inkSpanClasses(slug, wordIndex++)}>
            {token}
          </span>
        ),
      )}
    </>
  );
}
