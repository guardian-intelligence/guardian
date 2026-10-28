import { Link } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Letter } from "~/content/letters";
import { LetterSignature } from "~/features/letters/letter-signature";
import { markdownFromDom } from "~/features/letters/markdown-from-dom";
import {
  LETTER_POST_PAGE_PADDING_CLASS,
  LETTER_READING_COLUMN_CLASS,
  LETTER_TEXT_MEASURE_CLASS,
  LetterDate,
  LetterSalutation,
  letterProseClassName,
} from "~/features/letters/typography";

// The letter editor is the letter page itself: the same date, salutation,
// ink, ruled pitch, and sign-off, with the body made editable in place. What
// you type is what /letters/$slug renders once saved, because saving turns
// the edited page back into the markdown the page is rendered from
// (markdown-from-dom.ts). The only addition is the bar pinned to the bottom
// of the window, outside the page's flow.

export interface EditableFields {
  readonly title: string;
  readonly flare: string;
  readonly publishedAt: string;
  readonly status: "draft" | "published";
}

export type SaveResult =
  | { readonly ok: true; readonly rev: number }
  | { readonly ok: false; readonly error: string };

type SaveState =
  | { readonly kind: "clean" }
  | { readonly kind: "dirty" }
  | { readonly kind: "saving" }
  | { readonly kind: "saved"; readonly status: EditableFields["status"] }
  | { readonly kind: "error"; readonly message: string };

const META_CLASS =
  "font-mono text-[11px] font-medium uppercase tracking-[0.16em] text-[var(--treatment-muted-meta)]";

const FIELD_CLASS =
  "min-w-0 border-0 border-b border-[var(--treatment-rule-color)] bg-transparent px-0 py-1 font-display text-[15px] normal-case tracking-normal text-[var(--treatment-ink)] outline-none focus-visible:border-[var(--treatment-ink)]";

export function LetterEditor({
  letter,
  rev: initialRev,
  onSave,
}: {
  readonly letter: Letter;
  readonly rev: number;
  readonly onSave: (fields: EditableFields & { body: string; rev: number }) => Promise<SaveResult>;
}) {
  const bodyRef = useRef<HTMLDivElement>(null);
  const [fields, setFields] = useState<EditableFields>({
    title: letter.title,
    flare: letter.flare,
    publishedAt: letter.publishedAt,
    status: letter.status,
  });
  const [rev, setRev] = useState(initialRev);
  const [state, setState] = useState<SaveState>({ kind: "clean" });

  // Set once: React must never re-render the body's children, or every
  // keystroke would be thrown away. The ink matches the page because it is
  // the page's own lead + continuation HTML, each counted from word 0.
  const initialBody = useMemo(
    () => ({ __html: `${letter.leadHtml}${letter.continuationHtml}` || "<p><br></p>" }),
    [letter],
  );

  const edit = useCallback((patch: Partial<EditableFields>) => {
    setFields((current) => ({ ...current, ...patch }));
    setState({ kind: "dirty" });
  }, []);

  const save = useCallback(async () => {
    const body = bodyRef.current;
    if (!body) return;
    setState({ kind: "saving" });
    const result = await onSave({ ...fields, body: markdownFromDom(body), rev }).catch(
      (error: unknown): SaveResult => ({ ok: false, error: String(error) }),
    );
    if (result.ok) {
      setRev(result.rev);
      setState({ kind: "saved", status: fields.status });
    } else {
      setState({ kind: "error", message: result.error });
    }
  }, [fields, onSave, rev]);

  useEffect(() => {
    // New paragraphs are <p>, like the ones marked renders.
    document.execCommand("defaultParagraphSeparator", false, "p");
  }, []);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (!(event.metaKey || event.ctrlKey)) return;
      if (event.key === "s") {
        event.preventDefault();
        void save();
      } else if (event.key === "k" && bodyRef.current?.contains(document.activeElement)) {
        event.preventDefault();
        const href = window.prompt("Link to");
        if (href) document.execCommand("createLink", false, href);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [save]);

  const unsaved = state.kind === "dirty" || state.kind === "error";
  useEffect(() => {
    if (!unsaved) return;
    const onBeforeUnload = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", onBeforeUnload);
    return () => window.removeEventListener("beforeunload", onBeforeUnload);
  }, [unsaved]);

  const shown: Letter = { ...letter, ...fields };

  return (
    <>
      <article
        data-letter-transition-route="post"
        className={`${LETTER_READING_COLUMN_CLASS} ${LETTER_POST_PAGE_PADDING_CLASS}`}
      >
        <div className={LETTER_TEXT_MEASURE_CLASS}>
          <Link
            to="/letters/$slug"
            params={{ slug: letter.slug }}
            className={`-mx-3 mb-2 inline-flex min-h-11 items-center gap-2 px-3 no-underline ${META_CLASS}`}
          >
            <ArrowLeft aria-hidden="true" size={13} strokeWidth={1.75} />
            <span>Return</span>
          </Link>
          <LetterDate letter={shown} morph={false} />
          <LetterSalutation letter={shown} />
          <div data-letter-body className="mt-[var(--letters-line-pitch)]">
            <div
              ref={bodyRef}
              data-letter-editor
              aria-label="Letter body"
              contentEditable
              suppressContentEditableWarning
              spellCheck
              className={`${letterProseClassName} outline-none`}
              onInput={() => setState({ kind: "dirty" })}
              onPaste={(event) => {
                // Words only: formatting from elsewhere never reaches the page.
                event.preventDefault();
                document.execCommand(
                  "insertText",
                  false,
                  event.clipboardData.getData("text/plain"),
                );
              }}
              dangerouslySetInnerHTML={initialBody}
            />
          </div>
          <LetterSignature letter={shown} />
          {/* Room to scroll the last lines clear of the bar. */}
          <div aria-hidden className="h-28" />
        </div>
      </article>
      <EditorBar>
        <label className="flex min-w-[14rem] flex-[2] flex-col gap-0.5">
          <span>Title</span>
          <input
            className={FIELD_CLASS}
            value={fields.title}
            onChange={(event) => edit({ title: event.target.value })}
          />
        </label>
        <label className="flex min-w-[8rem] flex-1 flex-col gap-0.5">
          <span>Flare</span>
          <input
            className={FIELD_CLASS}
            value={fields.flare}
            onChange={(event) => edit({ flare: event.target.value })}
          />
        </label>
        <label className="flex flex-col gap-0.5">
          <span>Dated</span>
          <input
            type="date"
            className={FIELD_CLASS}
            value={fields.publishedAt}
            onChange={(event) => edit({ publishedAt: event.target.value })}
          />
        </label>
        <div role="group" aria-labelledby="letter-editor-status" className="flex flex-col gap-0.5">
          <span id="letter-editor-status">Status</span>
          <span className="flex gap-1">
            {(["draft", "published"] as const).map((status) => (
              <button
                key={status}
                type="button"
                aria-pressed={fields.status === status}
                onClick={() => edit({ status })}
                className={`px-2 py-1 ${META_CLASS} ${
                  fields.status === status
                    ? "bg-[var(--treatment-ink)] !text-[var(--treatment-ground)]"
                    : "border border-[var(--treatment-rule-color)]"
                }`}
              >
                {status}
              </button>
            ))}
          </span>
        </div>
        <span className="ml-auto flex items-center gap-3">
          <span role="status" className="normal-case tracking-normal">
            {describe(state)}
          </span>
          <button
            type="button"
            onClick={() => void save()}
            disabled={state.kind === "saving"}
            className={`px-3 py-2 ${META_CLASS} bg-[var(--treatment-ink)] !text-[var(--treatment-ground)] disabled:opacity-60`}
          >
            Save
          </button>
        </span>
      </EditorBar>
    </>
  );
}

function describe(state: SaveState): string {
  switch (state.kind) {
    case "clean":
      return "⌘S saves";
    case "dirty":
      return "Unsaved changes";
    case "saving":
      return "Saving…";
    case "saved":
      // The edge holds /letters pages for up to s-maxage (300s).
      return state.status === "published" ? "Saved · live within 5 minutes" : "Saved as draft";
    case "error":
      return state.message;
  }
}

function EditorBar({ children }: { readonly children: ReactNode }) {
  return (
    <aside
      aria-label="Letter editor"
      className={`fixed inset-x-0 bottom-0 z-50 border-t border-[var(--treatment-rule-color)] bg-[var(--treatment-ground)] ${META_CLASS}`}
    >
      <div
        className={`${LETTER_READING_COLUMN_CLASS} flex flex-wrap items-end gap-x-5 gap-y-2 py-3`}
      >
        {children}
      </div>
    </aside>
  );
}
