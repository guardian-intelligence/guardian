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
2. The browser enters the Guardian realm and the user chooses a configured
   social provider. Keycloak runs the provider broker flow and resolves or
   creates the Guardian account.
3. The exact Postflight callback exchanges the code from the server, verifies
   the ID token signature and claims against Keycloak JWKS, and issues a
   thirty-minute encrypted local session.
4. Product APIs validate the Guardian issuer and audience. An organization or
   repository action additionally calls the Authorization API with the
   Guardian subject and typed resource permission.

## Realm reconciliation

An empty database is initialized from the checked-in Guardian realm import.
Steady state is enforced through the Keycloak Admin REST API by the
`guardian-realm-reconciler` service account inside that realm. Its roles are
limited to realm settings, clients, and identity providers; it cannot manage
users, delete other realms, or administer the master realm.

The reconciler and confidential product clients use Keycloak Vault SPI
references backed by mounted Kubernetes Secrets. Their usable credentials do
not enter the Keycloak database or its backups. Temporary bootstrap
administrators are recovery artifacts, not runtime dependencies.

## Canary

The production canary is a fresh Chromium profile that performs the same
journey as a user: open Postflight, click **Sign in with Guardian**, enter the
GitHub machine account credentials and TOTP, return through the OIDC callback,
verify an authenticated Postflight session, sign out, and verify the local
session is gone. It does not use a direct grant or Keycloak admin API and it
does not simulate a broker callback.

An operator approves the OAuth App once in an interactive browser during
machine-account enrollment and verifies that it appears under the account's
authorized OAuth Apps. Scheduled runs use a fresh browser profile and fail if
GitHub unexpectedly requires interactive consent again. The canary runs every
15 minutes because GitHub limits a user/application/scope combination to ten
OAuth tokens per hour and deliberately forces reauthorization above that
limit.

The machine account is permanent and has no organization privileges. Its
password and TOTP seed live only in the production OpenBao scope. Its first
run completes Keycloak's broker-profile enrollment; an email collision or
account-linking prompt fails the canary instead of linking automatically.

The separate `digital-guardian-software` organization exercises the staging
Postflight GitHub App and CI runner path. It is not part of the customer-login
canary and grants the login machine account no organization access.
