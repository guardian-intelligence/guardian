import { readFileSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import * as v from "valibot";
import { LetterFrontmatterSchema } from "../letter-schema.ts";
import {
  FRONTMATTER_KEYS,
  type FrontmatterKey,
  type LetterRecord,
  parseLetterFile,
  serializeLetter,
} from "./serialize.ts";

// letters-cms: the Directus <-> repo sync for company-site letters.
//
//   node src/content/letters-cms/cli.ts push   # seed/update Directus from ./letters/*.md
//   node src/content/letters-cms/cli.ts pull   # mirror Directus back into ./letters/*.md
//
// Directus is the authoring source of truth; the repo files are the baked
// form the site builds from. push never deletes remote letters; pull mirrors
// exactly (writes changed files, deletes local letters Directus no longer
// has) so that after a pull, `git diff` is precisely the authoring delta.
//
// Auth (the Studio is port-forward-only, so the default URL is localhost):
//   DIRECTUS_URL      default http://127.0.0.1:8055
//   DIRECTUS_TOKEN    static token, or:
//   DIRECTUS_EMAIL / DIRECTUS_PASSWORD  admin login

const BASE_URL = process.env["DIRECTUS_URL"] ?? "http://127.0.0.1:8055";
const LETTERS_DIR = join(import.meta.dirname, "..", "letters");
const COLLECTION = "letters";

let accessToken: string | null = process.env["DIRECTUS_TOKEN"] ?? null;

async function login(): Promise<void> {
  if (accessToken) return;
  const email = process.env["DIRECTUS_EMAIL"];
  const password = process.env["DIRECTUS_PASSWORD"];
  if (!email || !password) {
    throw new Error("letters-cms: set DIRECTUS_TOKEN or DIRECTUS_EMAIL/DIRECTUS_PASSWORD");
  }
  const res = await fetch(`${BASE_URL}/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ email, password }),
  });
  if (!res.ok) throw new Error(`letters-cms: login failed: ${res.status} ${await res.text()}`);
  const json = (await res.json()) as { data: { access_token: string } };
  accessToken = json.data.access_token;
}

async function api(path: string, init?: RequestInit): Promise<Response> {
  return fetch(`${BASE_URL}${path}`, {
    ...init,
    headers: {
      authorization: `Bearer ${accessToken}`,
      "content-type": "application/json",
      ...init?.headers,
    },
  });
}

// One field per frontmatter key plus the markdown body; slug is the manual
// string primary key so upserts and file names agree by construction.
const COLLECTION_SPEC = {
  collection: COLLECTION,
  meta: {
    icon: "mail",
    note: "Company-site letters. Mirrored to git by letters-cms; the public site builds from the mirror, never from here.",
  },
  schema: {},
  fields: [
    {
      field: "slug",
      type: "string",
      schema: { is_primary_key: true, length: 128 },
      meta: { interface: "input", readonly: false },
    },
    { field: "title", type: "string", schema: {}, meta: { interface: "input" } },
    { field: "publishedAt", type: "date", schema: {}, meta: {} },
    {
      field: "flare",
      type: "string",
      schema: {},
      meta: {
        interface: "input",
        note: "Must be a substring of title; the OG card highlights it.",
      },
    },
    {
      field: "kind",
      type: "string",
      schema: {},
      meta: {
        interface: "select-dropdown",
        options: {
          choices: [
            { text: "dispatch", value: "dispatch" },
            { text: "correspondence", value: "correspondence" },
          ],
        },
      },
    },
    {
      field: "summary",
      type: "text",
      schema: {},
      meta: {
        interface: "input-multiline",
        note: "Empty = draft; the site hides letters without a summary.",
      },
    },
    {
      field: "author",
      type: "string",
      schema: {},
      meta: { interface: "input", note: "Provenance only, never rendered." },
    },
    {
      field: "authorTitle",
      type: "string",
      schema: {},
      meta: { interface: "input", note: "Provenance only, never rendered." },
    },
    {
      field: "description",
      type: "text",
      schema: {},
      meta: { interface: "input-multiline", note: "One-line account; doubles as OG/share text." },
    },
    {
      field: "note",
      type: "text",
      schema: {},
      meta: { interface: "input-multiline", note: "Author's longer context, JSON-LD only." },
    },
    { field: "body", type: "text", schema: {}, meta: { interface: "input-rich-text-md" } },
  ],
} as const;

async function ensureCollection(): Promise<void> {
  const existing = await api(`/collections/${COLLECTION}`);
  if (existing.ok) return;
  const created = await api("/collections", {
    method: "POST",
    body: JSON.stringify(COLLECTION_SPEC),
  });
  if (!created.ok) {
    throw new Error(
      `letters-cms: creating collection failed: ${created.status} ${await created.text()}`,
    );
  }
  console.error(`created collection "${COLLECTION}"`);
}

type Item = Partial<Record<FrontmatterKey, string | null>> & { slug: string; body: string | null };

function localLetters(): Map<string, { record: LetterRecord; text: string }> {
  const out = new Map<string, { record: LetterRecord; text: string }>();
  for (const name of readdirSync(LETTERS_DIR).filter((n) => n.endsWith(".md"))) {
    const text = readFileSync(join(LETTERS_DIR, name), "utf8");
    const record = parseLetterFile(text);
    v.parse(LetterFrontmatterSchema, record.frontmatter);
    const slug = record.frontmatter.slug;
    if (slug !== name.replace(/\.md$/, "")) {
      throw new Error(`letters-cms: ${name} slug frontmatter does not match its filename`);
    }
    out.set(slug ?? name, { record, text });
  }
  return out;
}

async function remoteSlugs(): Promise<Set<string>> {
  const res = await api(`/items/${COLLECTION}?limit=-1&fields=slug`);
  if (!res.ok)
    throw new Error(`letters-cms: listing items failed: ${res.status} ${await res.text()}`);
  const json = (await res.json()) as { data: { slug: string }[] };
  return new Set(json.data.map((d) => d.slug));
}

async function push(): Promise<void> {
  await ensureCollection();
  const local = localLetters();
  const remote = await remoteSlugs();
  for (const [slug, { record }] of local) {
    const item: Item = { slug, body: record.body };
    for (const key of FRONTMATTER_KEYS) {
      if (key === "slug") continue;
      item[key] = record.frontmatter[key] ?? null;
    }
    const res = remote.has(slug)
      ? await api(`/items/${COLLECTION}/${encodeURIComponent(slug)}`, {
          method: "PATCH",
          body: JSON.stringify(item),
        })
      : await api(`/items/${COLLECTION}`, { method: "POST", body: JSON.stringify(item) });
    if (!res.ok)
      throw new Error(`letters-cms: pushing ${slug} failed: ${res.status} ${await res.text()}`);
    console.error(`${remote.has(slug) ? "updated" : "created"} ${slug}`);
  }
  for (const slug of remote) {
    if (!local.has(slug)) console.error(`remote-only (left untouched): ${slug}`);
  }
}

async function pull(): Promise<void> {
  const res = await api(`/items/${COLLECTION}?limit=-1&sort=slug&fields=*`);
  if (!res.ok)
    throw new Error(`letters-cms: listing items failed: ${res.status} ${await res.text()}`);
  const { data } = (await res.json()) as { data: Item[] };
  const seen = new Set<string>();
  for (const item of data) {
    const frontmatter: Partial<Record<FrontmatterKey, string>> = {};
    for (const key of FRONTMATTER_KEYS) {
      const value = item[key];
      if (typeof value === "string" && value !== "") frontmatter[key] = value;
    }
    v.parse(LetterFrontmatterSchema, frontmatter);
    const text = serializeLetter({ frontmatter, body: item.body ?? "" });
    seen.add(item.slug);
    const path = join(LETTERS_DIR, `${item.slug}.md`);
    let current: string | null = null;
    try {
      current = readFileSync(path, "utf8");
    } catch {
      current = null;
    }
    if (current === text) {
      console.error(`unchanged ${item.slug}`);
    } else {
      writeFileSync(path, text);
      console.error(`${current === null ? "created" : "rewrote"} ${item.slug}`);
    }
  }
  for (const name of readdirSync(LETTERS_DIR).filter((n) => n.endsWith(".md"))) {
    const slug = name.replace(/\.md$/, "");
    if (!seen.has(slug)) {
      rmSync(join(LETTERS_DIR, name));
      console.error(`deleted ${slug} (no longer in Directus)`);
    }
  }
}

const command = process.argv[2];
await login();
if (command === "push") await push();
else if (command === "pull") await pull();
else {
  console.error("usage: node src/content/letters-cms/cli.ts <push|pull>");
  process.exit(2);
}
