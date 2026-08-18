# Letters CMS (Directus)

Company-site letters live in Directus (`tenant-guardian-prod`,
`deployments/products/prod/directus.yaml`) and nowhere else. The Studio is
the editor; the site's SSR server fetches published letters from the
Directus service at request time
(`src/company/web/src/content/letters.server.ts`). Content
never passes through git, so publishing an edit is saving it in the Studio.

## Contract

- Directus is the letters read path, deliberately: content edits must not
  mint commits or deploys. Availability is layered instead of duplicated:
  - the SSR server caches letters in memory (60s TTL) and serves its
    last-good fetch through any Directus failure;
  - Cloudflare edge-caches `/letters*` per the origin `Cache-Control`
    (`s-maxage=300, stale-while-revalidate=600, stale-if-error=86400` — the
    `letters_page_cache` rule in `guardian-mgmt-edge-policy`), so a full
    origin outage degrades to staleness for any page the edge has seen.
  - Worst case — pod cold start with Directus down — /letters fails, which
    the synthetic probes page on.
- Publishing is gated on `summary`: a letter without one is a draft. The
  anonymous role's permission filter (`setup-public-read`) only exposes
  letters with a summary, so drafts never leave the Studio — not to the
  site, not to `cms.guardianintelligence.org/items/letters` (which serves
  exactly the content already public on /letters).
- The Data Studio is routed at https://cms.guardianintelligence.org behind
  Keycloak SSO (client `directus`, customer realm). Public registration is
  off: an SSO login succeeds only for users pre-created in Directus via
  `setup-sso`, and those users carry the letters-only editor policy — no
  admin surface. The local admin account is ops/break-glass only and is
  reached by port-forward.
- Durability is the products Postgres cluster: nightly base backups + WAL
  archiving to R2 (`products-postgres-nightly`), plus Directus's own
  per-edit revision history for undo.

## Authoring loop

Open https://cms.guardianintelligence.org and continue with Keycloak — the
Guardian sign-in you already use. (Ops fallback: port-forward
`svc/directus 8055:80` and sign in as admin@guardianintelligence.org with
`Secret/directus-admin-credential`.)

Edit and save. Published pages pick the change up within ~1 minute at the
origin and within the edge TTL (~5 minutes) globally.

Drafting: leave `summary` empty while writing; fill it in to publish. To
preview a draft with the site's real typography, run the app dev server
against a port-forward with draft access:

```sh
kubectl port-forward -n tenant-guardian-prod svc/directus 8055:80 &
DIRECTUS_TOKEN=... DIRECTUS_INCLUDE_DRAFTS=true npx pnpm run dev
```

## Provisioning

Directus application config (the letters collection, field metadata, the
anonymous read grant) is code: the `directus-provisioner` Job
(`deployments/products/prod/directus-provisioner.yaml`) reconciles it
through the admin API on every config change, with the credential mounted
by the cluster — the realm-reconciler pattern. Change the script, bump its
checksum annotation, PR.

The one manual admin action is granting a person Studio access, because
editor emails are personal data that stays out of the repo. From
`src/company/web`, against a
port-forward with `DIRECTUS_EMAIL=admin@guardianintelligence.org
DIRECTUS_PASSWORD=...`:

- `node src/content/letters-cms/cli.ts setup-sso <email>` — provision the
  scoped editor and pre-create the Keycloak-provider user; the email must
  match the account's email claim in the customer realm.
