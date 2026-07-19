# Sign in with Guardian OAuth App

Use this procedure to create or update the GitHub OAuth App that Keycloak uses
as the GitHub social provider for one Guardian environment.

## Environment template

Set `ENV` to exactly `prod` or `staging`, then use the matching row without
editing the derived values:

| Value | `prod` | `staging` |
| - | - | - |
| `ENV_SUFFIX` | empty string | ` (Staging)` |
| `HOST` | `guardianintelligence.org` | `staging.guardianintelligence.org` |
| OAuth App name | `Sign in with Guardian` | `Sign in with Guardian (Staging)` |
| Homepage URL | `https://guardianintelligence.org` | `https://staging.guardianintelligence.org` |
| Authorization callback URL | `https://guardianintelligence.org/realms/guardianintelligence.org/broker/github/endpoint` | `https://staging.guardianintelligence.org/realms/guardianintelligence.org/broker/github/endpoint` |

The owner is `guardian-intelligence`. Each environment has one registration,
one client secret, and one exact callback. Do not add beta, gamma, localhost,
wildcard, or alternate callback URLs.

## Procedure

1. Start in a clean browser profile and sign in as an organization owner.
2. To update a registration, open:

   ```text
   https://github.com/organizations/guardian-intelligence/settings/applications/<SETTINGS_ID>
   ```

   To create one, open the organization settings, select **Developer
   settings → OAuth Apps → New OAuth App**, and confirm the owner shown by
   GitHub is `guardian-intelligence`.
3. Enter the exact name, homepage, and callback from the environment table.
   Leave **Enable Device Flow** off.
4. Before submitting, compare every character of the callback with the table.
   Save the registration.
5. Record the numeric settings ID from the settings-page URL and the Client ID
   from the page. These are public identifiers and belong in
   `src/infrastructure/deployments/iam/github-oauth-apps.yaml`.
6. For a new registration or an intentional secret rotation only, select
   **Generate a new client secret**.
7. **Human gate:** Copy the client secret directly into the secure custody
   record. GitHub shows a newly generated secret only once. Do not remove an
   existing secret until Keycloak is healthy on the replacement and the full
   browser canary passes.
8. Sign out and clear clipboard contents.

## Secret output

The custody value is named:

```text
<UPPERCASE_ENV>_GITHUB_CLIENT_SECRET=<GitHub OAuth App client secret>
```

The OpenBao destination is:

```text
guardian/guardian-mgmt/tenant-guardian-<ENV>/keycloak/github-oauth
```

with property `GITHUB_CLIENT_SECRET`. The Client ID remains in the static
registry; never commit the client secret.

## Acceptance record

```text
environment=<prod|staging>
owner=guardian-intelligence
settings_id=<numeric settings ID>
client_id=<public Client ID>
app_name=<exact derived name>
homepage_url=<exact derived homepage>
callback_url=<exact derived callback>
device_flow=off
custody_record=<identifier>
```

After the environment is deployed, its OIDC discovery document must return
HTTP 200 and a fresh-profile Sign in with Guardian canary must complete the
GitHub OAuth, Keycloak broker, product callback, authenticated-session, and
sign-out flow.
