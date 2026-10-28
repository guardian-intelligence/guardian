# Letters

Company-site letters live in the site's own store — the `letters` database in
the products Postgres (`src/company/deploy/cms/postgres.yaml`) — and nowhere
else. The site is also the editor: a letters editor opens
`https://guardianintelligence.org/letters/<slug>/edit` and edits the letter
page in place. Content never passes through git, so publishing an edit is
saving it.

## Contract

- The store is the letters read path, deliberately: content edits must not
  mint commits or deploys. Availability is layered instead of duplicated:
  - the SSR server caches letters in memory (60s TTL) and serves its
    last-good read through any database failure;
  - Cloudflare edge-caches `/letters*` per the origin `Cache-Control`
    (`s-maxage=300, stale-while-revalidate=600, stale-if-error=86400` — the
    `letters_page_cache` rule in `guardian-mgmt-edge-policy`), so a full
    origin outage degrades to staleness for any page the edge has seen. The
    editor route answers `no-store`, so it is never edge-cached.
  - Worst case — pod cold start with the database down — /letters fails,
    which the synthetic probes page on.
- The body is markdown, the only source of the letter's words and structure.
  The editor renders it exactly as `/letters/<slug>` does (same ink, ruling,
  sign-off) and turns the edited page back into markdown on save
  (`src/company/web/src/features/letters/markdown-from-dom.ts`).
- Publishing is gated on `status` (draft/published). Drafts never render on
  the live site; the editor shows them.
- `description` is the page's meta/OG description and JSON-LD description.
  It is not in the editor; the letter's opening words stand in.
- Saves are compare-and-swap on a revision number, so a stale tab can't
  overwrite a newer save, and every save appends the whole letter to
  `letter_revisions` (who saved it, when). Durability is the products
  Postgres cluster: nightly base backups + WAL archiving to R2
  (`products-postgres-nightly`).
- The site creates its schema on first use. An empty store seeds itself once
  from Directus's anonymous letters API (the move off Directus); that import
  and Directus itself are removed once prod runs on the store.

## Editing

Open `/letters/<slug>/edit`. Signing in goes through the site's Guardian
account (Keycloak, GitHub) — the same session Postflight uses. Editors are
the Keycloak account ids listed in `LETTERS_EDITOR_SUBJECTS`
(`src/infrastructure/deployments/company/prod/web.yaml`); a signed-in
account that isn't listed is shown its id on the edit page, to add there by
PR. Account ids are opaque; emails stay out of the repo.

Type into the page. ⌘B / ⌘I for emphasis, ⌘K for a link, Enter for a new
paragraph, ⌘S to save. Title, flare, date, and Draft/Published sit in the bar
at the bottom. A published save is live on the replica that took it at once,
on the others within a minute, and at the edge within ~5 minutes.

## Preview

```sh
src/company/web/scripts/preview.sh
```

Serves the site from that checkout at http://127.0.0.1:4252 with prod
letters, drafts included, via a port-forward to the products Postgres (needs
cluster access that can read `Secret/postgres-products-credentials`). Code
edits hot-reload; letter edits show up within a minute. The live site never
renders drafts.
