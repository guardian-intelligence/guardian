import { readFileSync } from "node:fs";
import postgres from "postgres";
import * as v from "valibot";

// The letters store: one row per letter in the products Postgres cluster
// (database `letters`), plus an append-only revision per save so any edit can
// be read back. The site owns the schema; it is created on first use.
//
// LETTERS_DATABASE_HOST      postgres-products-rw in-cluster (web.yaml)
// LETTERS_DATABASE_PORT      default 5432
// LETTERS_DATABASE_NAME      default letters
// LETTERS_DATABASE_USER      default letters
// LETTERS_DATABASE_PASSWORD[_FILE]
// LETTERS_DATABASE_SSL       "require" in-cluster: encrypted, no CA
//                            verification (the CNPG server cert is signed
//                            by an operator CA the pod doesn't carry)

export interface LetterRecord {
  readonly slug: string;
  readonly title: string;
  readonly flare: string;
  readonly kind: "dispatch" | "correspondence";
  readonly status: "draft" | "published";
  readonly publishedAt: string;
  readonly author: string;
  readonly authorTitle: string;
  readonly description: string;
  readonly note: string;
  readonly body: string;
  readonly rev: number;
}

export type LetterDraft = Omit<LetterRecord, "rev">;

function env(name: string, fallback: string): string {
  return process.env[name]?.trim() || fallback;
}

// Read on every connect, like the auth secrets: a rotated mount lands
// without a pod roll.
function password(): string {
  const file = process.env["LETTERS_DATABASE_PASSWORD_FILE"]?.trim();
  if (file) return readFileSync(file, "utf8").trim();
  return process.env["LETTERS_DATABASE_PASSWORD"] ?? "";
}

let client: postgres.Sql | null = null;
let ready: Promise<void> | null = null;

function sql(): postgres.Sql {
  client ??= postgres({
    host: env("LETTERS_DATABASE_HOST", "127.0.0.1"),
    port: Number(env("LETTERS_DATABASE_PORT", "5432")),
    database: env("LETTERS_DATABASE_NAME", "letters"),
    username: env("LETTERS_DATABASE_USER", "letters"),
    password,
    ssl:
      env("LETTERS_DATABASE_SSL", "disable") === "require" ? { rejectUnauthorized: false } : false,
    // Three replicas share the products cluster's connection budget
    // (postgres.yaml): one idle-reaped connection each covers a shelf this
    // size.
    max: 2,
    idle_timeout: 60,
    connect_timeout: 10,
  });
  return client;
}

async function ensureSchema(db: postgres.Sql): Promise<void> {
  await db`
    create table if not exists letters (
      slug          text primary key,
      title         text not null,
      flare         text not null,
      kind          text not null check (kind in ('dispatch', 'correspondence')),
      status        text not null check (status in ('draft', 'published')),
      published_at  date not null,
      author        text not null default '',
      author_title  text not null default '',
      description   text not null default '',
      note          text not null default '',
      body          text not null,
      rev           integer not null default 1,
      updated_at    timestamptz not null default now()
    )`;
  await db`
    create table if not exists letter_revisions (
      slug       text not null,
      rev        integer not null,
      letter     jsonb not null,
      saved_by   text not null,
      saved_at   timestamptz not null default now(),
      primary key (slug, rev)
    )`;
  await importFromDirectus(db);
}

// One-time move off Directus: an empty table is seeded from the published
// letters Directus serves anonymously (every letter it holds is published).
// Replicas racing on a cold start collide on the primary key and keep the
// first copy. Removed with Directus once prod has run on the store.
const DirectusItemSchema = v.object({
  slug: v.string(),
  title: v.string(),
  flare: v.string(),
  kind: v.picklist(["dispatch", "correspondence"]),
  status: v.picklist(["draft", "published"]),
  publishedAt: v.string(),
  author: v.nullish(v.string(), ""),
  authorTitle: v.nullish(v.string(), ""),
  description: v.nullish(v.string(), ""),
  note: v.nullish(v.string(), ""),
  body: v.nullish(v.string(), ""),
});

async function importFromDirectus(db: postgres.Sql): Promise<void> {
  const base = process.env["DIRECTUS_URL"]?.trim();
  if (!base) return;
  const [row] = await db<{ count: number }[]>`select count(*)::int as count from letters`;
  if ((row?.count ?? 0) > 0) return;
  const response = await fetch(`${base}/items/letters?limit=-1`, {
    headers: { accept: "application/json" },
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error(`letters: Directus import responded ${response.status}`);
  const { data } = v.parse(v.object({ data: v.array(DirectusItemSchema) }), await response.json());
  for (const item of data) {
    await insertImported(db, item);
  }
  console.error(JSON.stringify({ msg: "letters: imported from Directus", count: data.length }));
}

async function insertImported(db: postgres.Sql, letter: LetterDraft): Promise<void> {
  await db.begin(async (tx) => {
    const inserted = await tx`
      insert into letters ${tx(toRow(letter))}
      on conflict (slug) do nothing
      returning slug`;
    if (inserted.length === 0) return;
    await tx`
      insert into letter_revisions (slug, rev, letter, saved_by)
      values (${letter.slug}, 1, ${tx.json(letter as never)}, 'import:directus')`;
  });
}

function toRow(letter: LetterDraft) {
  return {
    slug: letter.slug,
    title: letter.title,
    flare: letter.flare,
    kind: letter.kind,
    status: letter.status,
    published_at: letter.publishedAt,
    author: letter.author,
    author_title: letter.authorTitle,
    description: letter.description,
    note: letter.note,
    body: letter.body,
  };
}

const RowSchema = v.object({
  slug: v.string(),
  title: v.string(),
  flare: v.string(),
  kind: v.picklist(["dispatch", "correspondence"]),
  status: v.picklist(["draft", "published"]),
  published_at: v.string(),
  author: v.string(),
  author_title: v.string(),
  description: v.string(),
  note: v.string(),
  body: v.string(),
  rev: v.number(),
});

function fromRow(raw: unknown): LetterRecord {
  const row = v.parse(RowSchema, raw);
  return {
    slug: row.slug,
    title: row.title,
    flare: row.flare,
    kind: row.kind,
    status: row.status,
    publishedAt: row.published_at,
    author: row.author,
    authorTitle: row.author_title,
    description: row.description,
    note: row.note,
    body: row.body,
    rev: row.rev,
  };
}

async function db(): Promise<postgres.Sql> {
  const connection = sql();
  ready ??= ensureSchema(connection).catch((error: unknown) => {
    ready = null;
    throw error;
  });
  await ready;
  return connection;
}

// Dates come back as YYYY-MM-DD text, the shape the schema and the page
// formatting expect.
const COLUMNS =
  "slug, title, flare, kind, status, to_char(published_at, 'YYYY-MM-DD') as published_at, author, author_title, description, note, body, rev";

export async function allLetterRecords(): Promise<readonly LetterRecord[]> {
  const connection = await db();
  const rows = await connection`select ${connection.unsafe(COLUMNS)} from letters`;
  return rows.map(fromRow);
}

export async function letterRecord(slug: string): Promise<LetterRecord | undefined> {
  const connection = await db();
  const rows = await connection`
    select ${connection.unsafe(COLUMNS)} from letters where slug = ${slug}`;
  return rows[0] ? fromRow(rows[0]) : undefined;
}

export class LetterConflictError extends Error {}

// Compare-and-swap on rev: a save made against an older copy (another tab,
// another device) is refused instead of silently overwriting the newer one.
export async function saveLetterRecord(
  letter: LetterDraft,
  expectedRev: number,
  savedBy: string,
): Promise<LetterRecord> {
  const connection = await db();
  return connection.begin(async (tx) => {
    const { slug, ...fields } = toRow(letter);
    const updated = await tx`
      update letters set ${tx(fields)}, rev = rev + 1, updated_at = now()
      where slug = ${slug} and rev = ${expectedRev}
      returning ${tx.unsafe(COLUMNS)}`;
    if (!updated[0])
      throw new LetterConflictError(`letter ${letter.slug} changed since it was opened`);
    const saved = fromRow(updated[0]);
    await tx`
      insert into letter_revisions (slug, rev, letter, saved_by)
      values (${saved.slug}, ${saved.rev}, ${tx.json(letter as never)}, ${savedBy})`;
    return saved;
  });
}
