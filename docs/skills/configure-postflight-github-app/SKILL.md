---
name: configure-postflight-github-app
description: Create or update the Postflight GitHub App for production or staging. Use for GitHub App branding, webhook, least-privilege permissions, credentials, custody, installation, and verification.
---

1. Set `ENV` to `prod` or `staging`. For `prod`, set `SUFFIX` to empty and
   `HOST` to `guardianintelligence.org`. For `staging`, set `SUFFIX` to
   ` (Staging)` and `HOST` to `staging.guardianintelligence.org`.
2. Set the name to `Postflight by Guardian${SUFFIX}`, homepage to
   `https://${HOST}/postflight`, and webhook URL to
   `https://${HOST}/api/v1/github/webhooks`.
3. Generate `/tmp/postflight-${ENV}.png` as a 512×512 PNG on `#0E0E0E`.
   Refer to the design packet at `https://guardianintelligence.org/design`
   for assets and branding.
4. Sign in as an owner of `guardian-intelligence`. Open the existing GitHub
   App settings page or **Organization settings → Developer settings → GitHub
   Apps → New GitHub App** if the app doesn't exist.
5. Enter the exact name and homepage from step 2. Leave callback and setup
   URLs empty; disable user authorization during installation and device
   flow.
6. Enable webhooks, set the exact webhook URL, generate one unique webhook
   secret, and subscribe only to **Workflow job**.
7. Set repository permissions to **Actions: read**, **Metadata: read**, and
   **Pull requests: read and write**. Set organization permissions to
   **Self-hosted runners: read and write**. Set every other permission to
   **No access**.
8. Select **Any account**, disable Marketplace listing, save, and upload
   `/tmp/postflight-${ENV}.png` as the App logo.
9. Record the public App ID, Client ID, slug, and App URL. Generate exactly one
   private key and no client secret.
10. Store the App ID, webhook secret, and private-key PEM in environment
    custody. Use `github_runner_app_prod_app_id`,
    `github_runner_app_prod_webhook_secret`, and
    `keys/postflight-runner.private-key.pem` for production; use
    `github_runner_app_staging_app_id`,
    `github_runner_app_staging_webhook_secret`, and
    `keys/postflight-runner-staging.private-key.pem` for staging.
11. Require the webhook URL to return HTTP 405 for an unauthenticated `GET`
    before installation.
12. Install production only in its intended customer organizations. Install
    staging only in the paid canary organization. Select only the explicit
    Postflight repositories.
13. Keep **Allow public repositories** disabled on the Postflight runner
    group.
14. Verify the public App with `gh api apps/<slug>` and the installation with
    `gh api orgs/<organization>/installation`; require the exact URLs,
    permissions, `workflow_job` event, selected repositories, and a successful
    signed webhook delivery.
