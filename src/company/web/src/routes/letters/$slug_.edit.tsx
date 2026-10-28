import { createFileRoute, notFound, redirect } from "@tanstack/react-router";
import { createServerFn } from "@tanstack/react-start";
import * as v from "valibot";
import { LetterFrontmatterSchema } from "~/content/letter-schema";
import { letterEditorAccess } from "~/content/letters-editor.server";
import { refreshLetters, renderLetter, setUncacheableHeader } from "~/content/letters.server";
import {
  LetterConflictError,
  letterRecord,
  saveLetterRecord,
} from "~/content/letters-store.server";
import { LetterEditor, type SaveResult } from "~/features/letters/letter-editor";

// /letters/$slug/edit — the letter page, editable in place, for letters
// editors (letters-editor.server.ts). Never cached: the edge rule for
// /letters* respects origin Cache-Control, and every response here is
// no-store.

const editLoader = createServerFn({ method: "GET" })
  .validator((slug: unknown) => v.parse(v.string(), slug))
  .handler(async ({ data: slug }) => {
    setUncacheableHeader();
    const access = await letterEditorAccess();
    if (access.kind === "signed-out") return { kind: "signed-out" as const };
    if (access.kind === "not-editor") {
      return { kind: "not-editor" as const, username: access.username, subject: access.subject };
    }
    const record = await letterRecord(slug);
    const letter = record ? renderLetter(record) : null;
    if (!record || !letter) return { kind: "missing" as const };
    return { kind: "editor" as const, letter, rev: record.rev };
  });

const SaveInputSchema = v.object({
  slug: v.string(),
  rev: v.number(),
  title: v.string(),
  flare: v.string(),
  publishedAt: v.string(),
  status: v.picklist(["draft", "published"]),
  body: v.string(),
});

const saveLetter = createServerFn({ method: "POST" })
  .validator((input: unknown) => v.parse(SaveInputSchema, input))
  .handler(async ({ data }): Promise<SaveResult> => {
    setUncacheableHeader();
    const access = await letterEditorAccess();
    if (access.kind !== "editor") {
      return { ok: false, error: "Signed out — sign in again to save." };
    }
    const current = await letterRecord(data.slug);
    if (!current) return { ok: false, error: "This letter no longer exists." };
    const { rev: _rev, ...unchanged } = current;
    const next = {
      ...unchanged,
      title: data.title.trim(),
      flare: data.flare.trim(),
      publishedAt: data.publishedAt,
      status: data.status,
      body: data.body,
    };
    const checked = v.safeParse(LetterFrontmatterSchema, next);
    if (!checked.success) {
      return { ok: false, error: checked.issues.map((issue) => issue.message).join("; ") };
    }
    try {
      const saved = await saveLetterRecord(next, data.rev, access.session.subject);
      await refreshLetters();
      return { ok: true, rev: saved.rev };
    } catch (error) {
      if (error instanceof LetterConflictError) {
        return { ok: false, error: "Changed elsewhere since you opened it — reload first." };
      }
      throw error;
    }
  });

export const Route = createFileRoute("/letters/$slug_/edit")({
  component: EditLetter,
  loader: async ({ params }) => {
    const result = await editLoader({ data: params.slug });
    if (result.kind === "signed-out") {
      throw redirect({
        href: `/postflight/auth/login?return_to=${encodeURIComponent(`/letters/${params.slug}/edit`)}`,
        reloadDocument: true,
        headers: { "cache-control": "no-store" },
      });
    }
    if (result.kind === "missing") throw notFound();
    return result;
  },
  head: ({ loaderData }) => ({
    meta: [
      {
        title:
          loaderData?.kind === "editor" ? `Editing ${loaderData.letter.title}` : "Letters editor",
      },
      { name: "robots", content: "noindex" },
    ],
  }),
});

function EditLetter() {
  const result = Route.useLoaderData();
  if (result.kind === "not-editor") {
    return (
      <main className="mx-auto max-w-xl px-[var(--chrome-inline-gap)] py-24 font-mono text-[12px] leading-relaxed text-[var(--treatment-muted-strong)]">
        <p>
          Signed in as {result.username}, which is not a letters editor. To grant access, add this
          account id to LETTERS_EDITOR_SUBJECTS:
        </p>
        <p className="mt-4 select-all text-[var(--treatment-ink)]">{result.subject}</p>
      </main>
    );
  }
  return (
    <LetterEditor
      letter={result.letter}
      rev={result.rev}
      onSave={(fields) => saveLetter({ data: { slug: result.letter.slug, ...fields } })}
    />
  );
}
