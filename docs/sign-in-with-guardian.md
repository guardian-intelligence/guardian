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
- The realm ships the `guardian-bounce` login theme: Keycloak never renders
  a visible page of its own except the device-flow consent screen, which is
  Guardian-branded (approving it is what marks the device code approved, so
  it cannot bounce). Denying the GitHub authorize prompt (or hitting a
  Keycloak URL without the hint) lands on the themed login page, which
  bounces straight back to Postflight; the device-flow terminal pages bounce
  to the product's approval page carrying the outcome
  (`?error=denied|expired|invalid|failed`), which renders the matching copy.
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
- OAuth App settings ID `3708383` is the only staging registration. Settings
  ID `3708386` is retired.
- The Postflight staging GitHub App is installed only in the canary
  organization.

There is no general-purpose Guardian GitHub App in this product boundary.
Use
[`configure-guardian-github-oauth`](skills/configure-guardian-github-oauth/SKILL.md)
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
`guardian-realm-reconciler` service account inside that realm. It holds the
realm's `realm-admin` composite — the workflows admin API accepts nothing
less — making it the realm's configuration root of trust, applied only
through reviewed configuration; it cannot delete other realms or administer
the master realm. The only other administrative principal is the canary
janitor, whose entire capability is a fine-grained grant on the
`canary-principals` group with scopes exactly `view-members` and
`manage-members`. The reconciler rewrites that grant on every run, so a
drifted grant shape heals at the next reconcile, and admin events stay
enabled as the audit trail for all user-store administration.

A realm update binds authentication flows by alias but never creates them,
and no realm update carries the user profile, broker mappers, groups, or
workflows — workflows are invisible even to realm import and export — so the
reconciler asserts the headless `broker-create-user-only` flow before the
provider loop binds it and applies each of the other surfaces through its
dedicated endpoint. The flow and its
execution are guarded independently, so a run that dies between the two
creates converges on retry instead of wedging on the half-made flow. The user profile
keeps `firstName` and `lastName` optional: a brokered GitHub account has no
guaranteed name, and a required name would let Keycloak demand one on its own
pages.

The reconciler and confidential product clients use Keycloak Vault SPI
references backed by mounted Kubernetes Secrets. Their usable credentials do
not enter the Keycloak database or its backups. Temporary bootstrap
administrators are recovery artifacts, not runtime dependencies.

## Alerting

Sign-in health is alerted on at the funnel: Keycloak's per-realm user event
metrics (`keycloak_user_events_total`) count every login attempt. Prod pages
`critical` when a majority of recent attempts fail (`GuardianSignInFailing`).
There is no synthetic traffic floor, so an absence of logins is not treated as
a signal; detection latency scales with real login volume.
