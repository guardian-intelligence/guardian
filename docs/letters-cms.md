# Letters CMS (Directus)

Company-site letters are authored in Directus and served from git. Directus
(`tenant-guardian-prod`, `deployments/products/prod/directus.yaml`) is the
authoring source of truth; `apps/guardianintelligence-web/src/content/letters/*.md`
is the baked mirror the site image builds from.

## Contract

- Directus is never on the public read path. The site bakes letters HTML at
  image-build time; readers are unaffected by any Directus outage.
- The Data Studio is routed at https://cms.guardianintelligence.org behind
  Keycloak SSO (client `directus`, customer realm). Public registration is
  off: an SSO login succeeds only for users pre-created in Directus via
  `setup-sso`, and those users carry the letters-only editor policy — no
  admin surface. The local admin account is ops/break-glass only and is
  reached by port-forward.
- Content flows one way per direction: `push` seeds/updates Directus from
  the repo and never deletes remote letters; `pull` mirrors Directus back
  exactly (including deletions), so after a pull `git diff` is precisely the
  authoring delta.
- Round-trip fidelity is enforced: `serialize.test.ts` fails the site image
  build if any letter file stops round-tripping byte-identically, which is
  what keeps a content no-op a build no-op.

## Authoring loop

Open https://cms.guardianintelligence.org and continue with Keycloak — the
Guardian sign-in you already use. (Ops fallback: port-forward
`svc/directus 8055:80` and sign in as admin@guardianintelligence.org with
`Secret/directus-admin-credential`.)

Edit letters in the Studio, then from `src/products/viteplus-monorepo/apps/guardianintelligence-web`:

```sh
DIRECTUS_EMAIL=admin@guardianintelligence.org DIRECTUS_PASSWORD=... \
  node src/content/letters-cms/cli.ts pull
```

Commit the diff on a branch and ship it through the normal PR loop. The
letter renders identically to its Directus form because the bytes are the
same ones the build always baked.

Drafting: a letter without a `summary` is a draft — it syncs and builds but
does not render on `/letters` or `/letters/$slug`. Fill in the summary in
Directus and pull to publish.

## Granting SSO access

`setup-sso` provisions the scoped editor artifacts (idempotent) and
pre-creates a Keycloak-provider user; the email must match the account's
email claim in the customer realm:

```sh
DIRECTUS_EMAIL=admin@guardianintelligence.org DIRECTUS_PASSWORD=... \
  node src/content/letters-cms/cli.ts setup-sso someone@example.com
```
