# Sign in with Guardian

Guardian is the customer identity provider at
`https://guardianintelligence.org/realms/guardianintelligence.org`. Products
are OIDC relying parties. GitHub and future social providers are brokered
connections to a Guardian account; they are not the account model.

## Invariants

- The Keycloak subject is the only login identity that product services
  persist or authorize.
- Upstream provider IDs and email addresses never become Guardian object IDs.
- Broker email is untrusted. An email collision never links accounts
  automatically; linking requires an authenticated Guardian session or proof
  of control of the existing account.
- Keycloak never renders a page on the login or logout path. The
  authorization request pins the GitHub broker with `kc_idp_hint`, first
  broker login creates the Guardian account with no user-facing steps and
  fails closed on any collision, and sign-out is RP-initiated with
  `id_token_hint` so no confirmation page appears. A sign-out without a
  recoverable ID token (expired or unsealable session cookie) clears the
  local session and returns to Postflight without visiting Keycloak, because
  Keycloak demands a confirmation page when the hint is missing.
- Known phase-1 exception: denying the GitHub authorize prompt returns
  `error=access_denied` to the broker endpoint, and Keycloak answers by
  rendering its own login page — a dead end, since the realm has no password
  or registration path. The realm conformance test tracks this exception;
  closing it requires a realm login theme (or equivalent) that returns the
  user to Postflight.
- Every web relying party is confidential, uses authorization code flow with
  PKCE S256, an exact callback, a server-side token exchange, and an encrypted
  HttpOnly `Secure` `SameSite=Lax` session cookie.
- Login availability depends on Keycloak, its database, and the selected
  upstream provider. It does not depend on SpiceDB.
- SpiceDB owns organization, project, repository, installation, and role
  authorization. Products send the Guardian subject to the typed
  Authorization API and fail closed on a missing or unavailable decision.
- Provider onboarding is declarative: one identity-provider document and one
  secret projection. Product code contains no provider-specific login path.
- Production and staging credentials are separate. Beta and gamma are not
  identity or Postflight application lanes.

## Product application inventory

| Surface | Production | Staging | Boundary |
| - | - | - | - |
| Sign in with Guardian GitHub OAuth App | Settings ID `3656712`, client ID `Ov23li9wlYzzt3mcfJ7V` | Settings ID `3708383`, client ID `Ov23liQCzyzCZ0Vr8SCf` | GitHub social login for the Guardian realm |
| Postflight GitHub App | [Postflight by Guardian](https://github.com/apps/postflight-by-guardian), App ID `3370540` | A dedicated `Postflight by Guardian (Staging)` App installed only in the canary organization | Webhooks, Actions API, and runner JIT configuration |

Before production activation:

- OAuth App settings ID `3656712` has homepage
  `https://guardianintelligence.org` and the single callback
  `https://guardianintelligence.org/realms/guardianintelligence.org/broker/github/endpoint`.
- OAuth App settings ID `3708383` has homepage
  `https://staging.guardianintelligence.org` and the single callback
  `https://staging.guardianintelligence.org/realms/guardianintelligence.org/broker/github/endpoint`.
- The GitHub machine account username, password, and TOTP seed exist at
  `guardian/guardian-mgmt/tenant-guardian-prod/keycloak/login-canary-github`
  in OpenBao.
- OAuth App settings ID `3708383` is the only staging registration. Settings
  ID `3708386` is retired.
- The Postflight staging GitHub App is installed only in the canary
  organization.

There is no general-purpose Guardian GitHub App in this product boundary.
Use
[`create-github-login-canary`](skills/create-github-login-canary/SKILL.md),
[`configure-guardian-github-oauth`](skills/configure-guardian-github-oauth/SKILL.md),
and
[`configure-postflight-github-app`](skills/configure-postflight-github-app/SKILL.md)
for the browser procedures.

## Request path

1. Postflight creates an encrypted ten-minute login transaction containing
   state, nonce, PKCE verifier, and a local return path.
2. The authorization request pins the GitHub broker with `kc_idp_hint`, so
   Keycloak redirects straight to GitHub without rendering a page. The
   headless first-broker-login flow resolves or creates the Guardian account
   and fails closed on any collision.
3. The exact Postflight callback exchanges the code from the server, verifies
   the ID token signature and claims against Keycloak JWKS, issues a
   thirty-minute encrypted local session, and lands the browser on the
   Postflight console at `/postflight/console`.
4. Product APIs validate the Guardian issuer and audience. An organization or
   repository action additionally calls the Authorization API with the
   Guardian subject and typed resource permission.
5. Sign-out is RP-initiated: the logout endpoint refuses cross-site
   triggers (by Fetch Metadata, falling back to an Origin/Referer
   same-origin check for browsers that send neither), then sends the stored
   ID token as `id_token_hint` with the registered
   `post_logout_redirect_uri` and `client_id`, so Keycloak ends the SSO
   session and returns to Postflight without a confirmation page. When no
   ID token is recoverable, the endpoint clears the local session and
   returns straight to Postflight; the orphaned SSO session ends at its
   idle timeout.

## Realm reconciliation

An empty database is initialized from the checked-in Guardian realm import.
Steady state is enforced through the Keycloak Admin REST API by the
`guardian-realm-reconciler` service account inside that realm. Its roles are
limited to realm settings, clients, and identity providers; it cannot manage
users, delete other realms, or administer the master realm.

A realm update binds authentication flows by alias but never creates them,
and no realm update carries the user profile, so the reconciler asserts the
headless `broker-create-user-only` flow before the provider loop binds it and
applies the user profile through its dedicated endpoint. The flow and its
execution are guarded independently, so a run that dies between the two
creates converges on retry instead of wedging on the half-made flow. The user profile
keeps `firstName` and `lastName` optional: a brokered GitHub account has no
guaranteed name, and a required name would let Keycloak demand one on its own
pages.

The reconciler and confidential product clients use Keycloak Vault SPI
references backed by mounted Kubernetes Secrets. Their usable credentials do
not enter the Keycloak database or its backups. Temporary bootstrap
administrators are recovery artifacts, not runtime dependencies.

## Canary

The production canary is a fresh Chromium profile that performs the same
journey as a user: open Postflight, click **Sign in with GitHub**, land
directly on github.com, enter the GitHub machine account credentials and
TOTP, return through the OIDC callback to the Postflight console, verify an
authenticated Postflight session, sign out, and verify the local session is
gone. Any rendered Keycloak page at any step fails the run. It does not use
a direct grant or Keycloak admin API and it does not simulate a broker
callback.

An operator approves the OAuth App once in an interactive browser during
machine-account enrollment and verifies that it appears under the account's
authorized OAuth Apps. Scheduled runs use a fresh browser profile and fail if
GitHub unexpectedly requires interactive consent again. The canary runs every
15 minutes because GitHub limits a user/application/scope combination to ten
OAuth tokens per hour and deliberately forces reauthorization above that
limit.

The machine account is permanent and has no organization privileges. Its
password and TOTP seed live only in the production OpenBao scope. Its first
run creates the Guardian account headlessly; an email collision or
account-linking prompt fails the canary instead of linking automatically.

The separate `digital-guardian-software` organization exercises the staging
Postflight GitHub App and CI runner path. It is not part of the customer-login
canary and grants the login machine account no organization access.
