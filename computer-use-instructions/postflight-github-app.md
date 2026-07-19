# Postflight GitHub App

Use this procedure to create the Postflight GitHub App for one environment.
The production and staging Apps have different credentials and installation
sets.

## Environment template

Set `ENV` to exactly `prod` or `staging`, then use the matching row:

| Value | `prod` | `staging` |
| - | - | - |
| `ENV_SUFFIX` | empty string | ` (Staging)` |
| `HOST` | `guardianintelligence.org` | `staging.guardianintelligence.org` |
| GitHub App name | `Postflight by Guardian` | `Postflight by Guardian (Staging)` |
| Homepage URL | `https://guardianintelligence.org/postflight` | `https://staging.guardianintelligence.org/postflight` |
| Webhook URL | `https://guardianintelligence.org/api/v1/github/webhooks` | `https://staging.guardianintelligence.org/api/v1/github/webhooks` |
| Installation target | production customer organizations | dedicated staging canary organization only |

The owning organization is `guardian-intelligence`. Do not create a
general-purpose Guardian App.

## Registration

1. Start in a clean browser profile and sign in as an organization owner.
2. Open the organization settings and select **Developer settings → GitHub
   Apps → New GitHub App**. Confirm the owner is `guardian-intelligence`.
3. Enter the exact App name and homepage from the environment table.
4. Leave the callback URL and setup URL empty. Turn off **Request user
   authorization (OAuth) during installation** and **Enable Device Flow**.
5. Under **Webhook**, select **Active**, enter the exact webhook URL, and
   generate a unique high-entropy webhook secret.
6. **Human gate:** Store the webhook secret directly in the environment's
   custody record. Do not place it in the prompt or clipboard history.
7. Set repository permissions:
   - **Actions:** Read-only
   - **Metadata:** Read-only
   - **Pull requests:** Read and write
8. Set organization permissions:
   - **Self-hosted runners:** Read and write
9. Set every other repository, organization, and account permission to **No
   access**.
10. Subscribe only to **Workflow job** events.
11. Select **Any account** for installability so the staging App can be
    installed in the separately owned canary organization. Do not list the App
    in GitHub Marketplace.
12. Re-read the environment table and permissions, then create the App.

## Credentials

1. Record the App ID, Client ID, public App URL, and slug. These identifiers
   are not secret.
2. Do not generate a client secret; Postflight does not use GitHub
   user-to-server OAuth.
3. Under **Private keys**, select **Generate a private key** once.
4. **Human gate:** Move the downloaded PEM directly into the environment's
   encrypted custody record. GitHub retains only the public key. Remove the
   browser download and clear clipboard contents.
5. Record the webhook secret and private-key custody identifiers alongside
   the public IDs. Never paste either secret into the repository.

## Custody output

Keep the public App ID and two secret values under environment-specific names:

| Value | `prod` | `staging` |
| - | - | - |
| App ID | `github_runner_app_prod_app_id` | `github_runner_app_staging_app_id` |
| Webhook secret | `github_runner_app_prod_webhook_secret` | `github_runner_app_staging_webhook_secret` |
| Private-key file | `keys/postflight-runner.private-key.pem` | `keys/postflight-runner-staging.private-key.pem` |

The production importer consumes `github_runner_app_prod_app_id`,
`github_runner_app_prod_webhook_secret`, and the base64 encoding of the
production PEM as `github_runner_app_prod_private_key_b64`. Staging values
remain in custody until a staging Postflight consumer and its OpenBao
projection are declared in Git. Do not seed them through an ad hoc cluster
write.

## Installation

1. Do not install the App until an unauthenticated `GET` to its configured
   webhook URL reaches Postflight and returns HTTP 405 rather than 404.
2. From the App settings page, select **Install App** and choose the exact
   installation target from the environment table.
3. Select **Only select repositories** and grant only the explicit Postflight
   canary or customer repositories.
4. For staging, stop if the target is not the dedicated canary organization.
   For production, do not use the staging canary organization.
5. Confirm the organization runner group used by Postflight has **Allow public
   repositories** off.

## Acceptance record

```text
environment=<prod|staging>
owner=guardian-intelligence
app_name=<exact derived name>
app_id=<numeric App ID>
client_id=<public Client ID>
app_slug=<slug>
homepage_url=<exact derived homepage>
webhook_url=<exact derived webhook>
webhook_active=yes
events=workflow_job
repository_permissions=actions:read,metadata:read,pull_requests:write
organization_permissions=organization_self_hosted_runners:write
all_other_permissions=no_access
private_keys=1
client_secrets=0
installation_account=<exact organization>
repository_selection=selected
allow_public_repositories=off
custody_record=<identifier>
```

Verify the public registration with:

```sh
gh api apps/<app-slug> \
  --jq '{id,slug,name,external_url,permissions,events,owner:.owner.login}'
```

Verify the target installation as an owner of that organization:

```sh
gh api orgs/<installation-account>/installation \
  --jq '{id,app_id,app_slug,repository_selection,permissions,events}'
```

The first signed `workflow_job` delivery must return 2xx before the App is
considered active.
