# GitHub Apps

Below list is 1p apps only. Some 3p apps are used e.g. Blacksmith to benchmark our CI against blackmsith. To list all:

```sh
gh api --paginate orgs/guardian-intelligence/installations \
  --jq '.installations[] | {app_id, app_slug, id, repository_selection, permissions, events}'
```

For each slug, public App metadata is available with:

```sh
gh api apps/<app-slug>
```

## Inventory

| App | App ID | Installation ID | Repository access | Purpose |
| - | -: | -: | - | - |
| [Postflight by Guardian](https://github.com/apps/postflight-by-guardian) | `3370540` | `123769944` | Selected repositories | Postflight's GitHub control plane: receives `workflow_job` webhooks and manages the Actions and runner resources needed to execute customer CI. |
| Postflight (Staging) | Pending owner creation | Canary organization only | Selected repositories | Exercises installation, webhook, Actions, and runner flows without using production App credentials. |
| [Guardian Promotions](https://github.com/apps/guardian-promotions) | `4206397` | `144138265` | All repositories | Gives Kargo a distinct bot identity for opening promotion PRs and arming their automerge. |
| [guardian-renovate](https://github.com/apps/guardian-renovate) | `4260384` | `145549950` | Selected repositories | Runs Renovate as a distinct bot identity so dependency commits and PRs trigger the normal validation workflows. |
| [guardian-platform-app](https://github.com/apps/guardian-platform-app) | `4276780` | `145993975` | Selected repositories | Shared non-human identity for GitHub API automation that does not need its own installation boundary. Consumers mint short-lived installation tokens instead of using personal access tokens. |

## Postflight by Guardian

The deployed control plane verifies GitHub webhooks, records deliveries, and
calls the GitHub API. Each signed delivery carries its installation identity;
that identity follows the job through token minting, scheduling, JIT runner
configuration, reconciliation, and PR comments.

- [Control-plane source](src/services/postflight/controlplane/)
- [Webhook handling](src/services/postflight/controlplane/webhook.go)
- [GitHub API client](src/services/postflight/controlplane/github_api.go)
- [Deployment](src/infrastructure/deployments/postflight-runner/controlplane.yaml)
- [OpenBao-backed App credentials](src/infrastructure/deployments/postflight-runner/secrets.yaml)
- [Product and GitHub App contract](docs/postflight-product.md)

Live permissions are Actions, checks, contents, pull requests, workflows, and
organization self-hosted runners read/write as applicable; organization
members and metadata read. The App subscribes to `workflow_job` events.
Production and staging use distinct GitHub Apps and installation sets. There
is no general-purpose Guardian App in the Postflight product boundary.

## Guardian Promotions

Kargo uses this App to authenticate Git operations and open image-promotion
PRs. A GitHub workflow then mints a second short-lived App token to arm
automerge. Required checks and branch protection remain the merge authority;
the bot itself is untrusted.

- [Kargo promotion pipelines](src/infrastructure/deployments/guardian/promotion/pipelines/)
- [OpenBao-backed Kargo credential](src/infrastructure/deployments/guardian/promotion/pipelines/secrets.yaml)
- [Promotion automerge workflow](.github/workflows/promotion-automerge.yml)
- [Supply-chain design](docs/supply-chain-design.md)

Live permissions are contents and pull requests write plus metadata read. The
App has no webhook subscriptions.

## guardian-renovate

The scheduled workflow mints a short-lived installation token and runs
Renovate. `renovate.json5` is the dependency policy; the workflow only owns
scheduling and execution.

- [Renovate policy](renovate.json5)
- [Scheduled Renovate workflow](.github/workflows/renovate.yml)
- [Dependency-management policy](docs/dependency-management.md)
- [Configuration validation](.github/scripts/check-renovate-config.sh)

Live permissions are checks, contents, issues, pull requests, statuses, and
workflows write; vulnerability alerts read. The App has no webhook
subscriptions.

## guardian-platform-app

This is the default identity for GitHub-facing platform automation. Its
private key lives in OpenBao, and consumers exchange App JWTs minted by the
in-cluster minter endpoint for one-hour installation tokens. The credentials and exception policy are documented in
[GitHub credentials: one App, short-lived tokens](docs/secrets.md#github-credentials-one-app-short-lived-tokens).

No repository consumer currently references this App's ID or private key.
Live permissions are contents, pull requests, and statuses write; checks read;
packages write; and metadata read. The App has no webhook subscriptions.
GitHub App `packages: write` does not provide the GHCR push path; the standing
GHCR exception is documented alongside the App.

## Refreshing this inventory

An organization owner with `read:org` can list the installations with:
