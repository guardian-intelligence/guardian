# Postflight lifecycle canaries and the state-wipe fabric

Canaries must walk the same lifecycle real users do — including churn. Walking
churn requires deleting canary state on both sides of the trust boundary: our
systems (Keycloak user, product data) and GitHub's (OAuth grant, and later App
installations). This document is the design for doing that safely as the
product surface grows, and for the CLI-first onboarding those canaries will
exercise. The web console is deliberately secondary: the CLI is the product's
front door.

## Invariants

- **Canaries never hold destructive credentials.** All state deletion and
  revocation goes through the janitor (below). A canary that can only walk
  journeys can fail without blast radius.
- **The janitor refuses everything that is not a canary principal.** Its
  allowlist of canary identities (Keycloak usernames and GitHub logins) is
  compiled from IaC and pinned by a conformance test. Wiping a non-canary user
  is not a permission failure; it is structurally impossible input.
- **Wipes are idempotent.** Absent state is success. A canary that crashed
  mid-run must be able to self-heal by wiping preconditions at the start of
  the next run, never by human cleanup.
- **No Keycloak page renders in any canary journey**, including recovery and
  reauth paths ([sign-in-with-guardian.md](sign-in-with-guardian.md)).
- **Prod semantics stay honest.** The janitor exists because we would never
  delete a prod user. Nothing in the wipe fabric may be reachable from
  product code paths.

## The janitor

A small internal service, one per stage, owning every destructive
canary-state operation.

Interface: `POST /wipe` with a canary principal and a set of surfaces, e.g.
`{"principal": "canary-churn-1", "surfaces": ["identity", "github-grant"]}`.
Each surface is a registered wiper module:

| Surface | Action | Credential held by janitor |
| - | - | - |
| `identity` | Delete the Keycloak user (federated link dies with it) | service-account client with `manage-users` |
| `github-grant` | `DELETE /applications/{client_id}/grant` for the canary's GitHub account | Sign-in OAuth App client credentials |
| `app-installations` (future) | Uninstall the Postflight GitHub App from canary orgs/repos | App JWT |
| `product-data` (future) | Purge canary rows from product stores | scoped DB role |

Adding a product surface means adding a wiper module and its conformance
test; canaries opt into surfaces by name. Wipes are synchronous API calls
(seconds, not minutes) so they fit inside canary cadence budgets. Every wipe
is audit-logged with principal, surfaces, caller, and outcome.

The janitor is the only component whose credential set grows with the canary
program. That concentration is the point: one place to review, one place to
allowlist, one place to audit.

## Canary matrix

### 1. Churn canary (new user, closed loop)

Precondition: wipe `identity` + `github-grant` (self-healing if a prior run
died mid-loop). Then:

1. Sign in from nothing — this walks `broker-create-user-only` live, which
   the returning-user canary structurally cannot (its account already
   exists). First-time invisibility stops being config-pinned and becomes
   continuously proven.
2. Assert onboarding surface, then console state (as those are built).
3. Revoke the GitHub grant via the janitor — the churning-user simulation.
4. Assert fail-closed everywhere: GitHub-proxied operations refuse with a
   reauth signal, the web session grants nothing GitHub-side, and the
   recovery path renders no Keycloak page.
5. Wipe both surfaces, assert clean (login would re-create), loop closed.

Cadence: every 30–60 minutes, staggered against the returning-user canary so
a shared-cause failure separates from a journey-specific one.

### 2. Returning-user canary

The existing 15-minute login canary, unchanged in role: sign in, assert
console, sign out. No wipe. As the console grows real interactions, they are
asserted here first — this canary is the "is the product up" signal and must
stay cheap and boring.

### 3. CLI onboarding canary (deterministic v1)

A container that does what an agent will be instructed to do:

1. Download the released CLI artifact and verify its signature (the registry
   sovereignty lane's stock-cosign verification is the assertion, not a
   convenience).
2. `postflight auth login` → device flow → approval leg completed by the
   canary's browser through the normal invisible web sign-in.
3. Assert the account was created (no separate signup step existed).
4. Exercise the org/repo verbs against our API plane as those land:
   `postflight orgs list`, connect, describe, disconnect — asserting our API
   proxies GitHub with the granted authority and never has the CLI talk to
   GitHub directly.
5. Janitor wipe, loop closed.

The future agent-matrix version (top-N models running SKILL.md and being
judged on both actions and user-facing consent language) replaces step 2's
"container does" with "model is instructed to" — same journey, same janitor,
different driver. It needs the SKILL.md and a judging harness; nothing in
this design blocks on it, and everything in it is reused by it.

## Revocation semantics: what "immediately unable" means

GitHub sends no webhook when a user revokes an OAuth App grant. "Immediate"
therefore cannot mean push-invalidation of our session; it means **no
GitHub-derived authority is ever exercised without live verification**. The
sealed web session may outlive the grant, but it only proves who the user
is — every operation that reaches GitHub does so with tokens whose validity
GitHub itself enforces, and failure is surfaced as a reauth requirement, not
an error page.

The churn canary asserts this behaviorally (step 4), which is stronger than
asserting our bookkeeping: it proves the property even when our records are
stale.

Upgrade path, explicitly not a v1 dependency: moving sign-in identity from
the OAuth App to a GitHub App user authorization would buy us the
`github_app_authorization` revocation webhook and true push-invalidation.
That is a separate migration with its own doctrine implications; this design
only requires that nothing here forecloses it.

## CLI auth design pins

- **Verbs follow `gh`**, which is the pattern agents already know:
  `postflight auth login [--provider github] [--web]`, `auth status`,
  `auth logout`, then `orgs`/`repos` nouns with `list|describe|connect|
  disconnect`. Device flow is the default (agents are usually headless);
  `--web` does loopback authorization-code + PKCE for humans at a browser.
- **There is no `signup` verb.** First login creates the Guardian account
  under the hood via `broker-create-user-only`, exactly as the web does. The
  account-creation consent language ("this creates a Guardian Intelligence
  account and links your GitHub…", the requested permissions and why) is the
  CLI's pre-flight output and the SKILL.md's instruction to relay it — not a
  separate verb whose state the canaries would then have to cover.
- **The device-flow approval leg goes through our page, not Keycloak's.**
  Keycloak's device-verification page is Keycloak-rendered, and
  `kc_idp_hint` does not survive the device hop, so the stock flow violates
  sign-in doctrine twice. The CLI's verification URL is `/postflight/device`:
  app-rendered, session-guarded by the normal invisible web sign-in, with the
  BFF completing Keycloak's device verification server-side. This needs one
  spike to confirm the completion call; the fallback is rendering Keycloak's
  device page under the planned bounce/minimal theme — tolerable, but it
  makes the theme a dependency instead of an option, so the spike comes
  first.
- **The CLI never talks to GitHub.** Org and repo operations go to our API
  plane, which proxies with the authority the user granted at sign-in (and,
  for repo access, the Postflight App installation). This keeps the
  permission story auditable in one place and keeps the CLI surface stable
  when GitHub's API shifts.

## Build order

1. Janitor with `identity` + `github-grant` wipers; churn canary. This
   closes the standing gap that first-broker-login is never exercised live.
2. `postflight auth login` (device flow + `/postflight/device` spike) and
   the deterministic CLI canary.
3. Wiper modules alongside each product surface as it lands (orgs/repos =
   App installations).
4. Agent-matrix canary once the SKILL.md and judging harness exist.
