# Letters CMS (Directus)

Company-site letters are authored in Directus and served from git. Directus
(`tenant-guardian-prod`, `deployments/products/prod/directus.yaml`) is the
authoring source of truth; `apps/guardianintelligence-web/src/content/letters/*.md`
is the baked mirror the site image builds from.

## Contract

- Directus is never on the public read path. The site bakes letters HTML at
  image-build time; readers are unaffected by any Directus outage.
- The Data Studio is not publicly routed (Keycloak admin-console precedent).
  Reach it only by port-forward.
- Content flows one way per direction: `push` seeds/updates Directus from
  the repo and never deletes remote letters; `pull` mirrors Directus back
  exactly (including deletions), so after a pull `git diff` is precisely the
  authoring delta.
- Round-trip fidelity is enforced: `serialize.test.ts` fails the site image
  build if any letter file stops round-tripping byte-identically, which is
  what keeps a content no-op a build no-op.

## Authoring loop

```sh
kubectl port-forward -n tenant-guardian-prod svc/directus 8055:80
# sign in at http://127.0.0.1:8055 as admin@guardianintelligence.org;
# password: kubectl get secret -n tenant-guardian-prod directus-admin-credential \
#   -o jsonpath='{.data.password}' | base64 -d
```

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
