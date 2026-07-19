---
name: configure-guardian-github-oauth
description: Create or update the GitHub OAuth App used as the GitHub social provider for Sign in with Guardian. Use for production or staging OAuth registration, branding, callback, client credentials, custody, and registry changes.
---

1. Set `ENV` to `prod` or `staging`. For `prod`, set `SUFFIX` to empty and
   `HOST` to `guardianintelligence.org`. For `staging`, set `SUFFIX` to
   ` (Staging)` and `HOST` to `staging.guardianintelligence.org`.
2. Set the name to `Sign in with Guardian${SUFFIX}`, homepage to
   `https://${HOST}`, and callback to
   `https://${HOST}/realms/guardianintelligence.org/broker/github/endpoint`.
3. Generate `/tmp/sign-in-with-guardian-${ENV}.png` as a 512×512 PNG on
   `#0E0E0E`. Use
   `src/products/viteplus-monorepo/apps/guardianintelligence-web/public/favicon.svg`
   as the immutable mark source; preserve its exact viewBox, wings path, and
   aspect ratio. Render it 320×320, centered at `(256,176)`, without
   nonuniform scaling. Add `SIGN IN WITH GUARDIAN` centered below it in
   uppercase white Geist Semibold with `0.14em` tracking; add a second
   `STAGING` line only when `ENV=staging`. Verify every label and the square
   dimensions.
4. Sign in as an owner of `guardian-intelligence`. Open the existing OAuth
   App settings page or **Organization settings → Developer settings → OAuth
   Apps → New OAuth App**.
5. Enter the exact name, homepage, and callback from step 2; leave device flow
   disabled; save.
6. Upload `/tmp/sign-in-with-guardian-${ENV}.png` as the application logo.
7. Record the settings ID from the URL and the public Client ID. Generate one
   client secret only for a new registration or intentional rotation.
8. Store the client secret as
   `${UPPERCASE_ENV}_GITHUB_CLIENT_SECRET` in custody for
   `guardian/guardian-mgmt/tenant-guardian-${ENV}/keycloak/github-oauth`.
9. Update
   `src/infrastructure/deployments/iam/github-oauth-apps.yaml` with the exact
   name, settings ID, Client ID, homepage, callback, realm
   `guardianintelligence.org`, provider alias `github`, and active status.
10. Keep exactly one production and one staging registration; remove every
    beta, gamma, localhost, wildcard, alternate-callback, or duplicate
    registration.
