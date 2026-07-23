---
name: sweep-github-app-permissions
description: Reconcile every GitHub App owned by or installed on guardian-intelligence through an organization owner's signed-in browser, enforce the committed least-privilege permission and repository boundaries, and accept only matching first-party permission updates.
---

# Computer-Use Agent GitHub Permissions Sweep Manual

Use this manual from the operator's already signed-in browser. Make every
state-changing operation through the GitHub UI. Terminal commands in this
manual are read-only acceptance checks, not a second control plane.

## Completion contract

A sweep is complete only when:

1. Every App owned by `guardian-intelligence` and every App installed on the
   organization appears exactly once in the run ledger.
2. Every first-party App matches the permission, event, and repository
   baselines below. Every unlisted permission is **No access**.
3. Every third-party App has an evidenced purpose, the narrowest repository
   selection that supports that purpose, and no unreviewed permission update.
4. Every pending permission update from an App owned by
   `guardian-intelligence` has either been accepted because its complete delta
   matches this manual or recorded as a blocker.
5. Reloading the installed Apps page shows no pending first-party request.

Do not report success with an unexplained App, permission, event, repository,
or request in the ledger.

## Safety boundaries

- Confirm that the browser is signed in as an owner of
  `guardian-intelligence`. Stop if **Settings** or **Developer settings** is
  unavailable.
- Never reveal a private key, client secret, webhook secret, password,
  passkey, recovery code, or TOTP setting during this sweep. The only secret
  mutation this manual permits is re-entering Postflight's existing
  OpenBao-held webhook secret into the GitHub App when the run explicitly
  requests webhook-secret reconciliation. Keep the value out of chat, tool
  output, argv, shell history, and files.
- Do not generate credentials, change App ownership or visibility, list an App
  in Marketplace, suspend an installation, uninstall an App, or delete an
  App. Those are separate operations.
- Do not approve a permission or event merely because GitHub presents an
  **Accept** button. The full requested state must match this manual.
- A run request that asks only for an audit does not authorize acceptance. The
  run must explicitly ask to reconcile permissions and accept matching
  internal updates before the agent uses the final acceptance control.
- "Internal request" means the App owner displayed by GitHub is exactly
  `guardian-intelligence`. Treat OpenAI, Blacksmith, Devin, and every other
  owner as third party even when the App is intentionally installed.
- If an unexpected third-party request is pending, leave it pending and record
  the App, owner, old access, requested access, and affected repositories as a
  blocker. GitHub Apps do not support accepting only part of a third-party
  permission bundle.

## First-party baseline

GitHub may display `write` as **Read and write** and
`vulnerability_alerts` as **Dependabot alerts**. Metadata read access is
mandatory for Apps with repository permissions. Every permission not named in
this table is **No access**.

| App | App ID | Repository permissions | Organization permissions | Account permissions | Webhook |
| - | -: | - | - | - | - |
| `Postflight by Guardian` | `3370540` | Actions: read; Metadata: read; Pull requests: write | Self-hosted runners: write | None | Active; only `Workflow job` |
| `Postflight by Guardian (Staging)` | Record in the environment inventory | Actions: read; Metadata: read; Pull requests: write | Self-hosted runners: write | None | Active; only `Workflow job` |
| `Guardian Promotions` | `4206397` | Contents: write; Metadata: read; Pull requests: write | None | None | Inactive; no events |
| `guardian-renovate` | `4260384` | Administration: read; Checks: write; Contents: write; Dependabot alerts: read; Issues: write; Metadata: read; Pull requests: write; Commit statuses: write; Workflows: write | Members: read | None | Inactive; no events |
| `guardian-platform-app` | `4276780` | Checks: read; Contents: write; Metadata: read; Packages: write; Pull requests: write; Commit statuses: write | None | None | Inactive; no events |

Additional invariants:

- User authorization during installation and Device Flow are disabled for all
  first-party Apps.
- `Postflight by Guardian` has these exact production General settings:
  - Homepage URL: `https://guardianintelligence.org/postflight`
  - Callback URLs: exactly
    `https://guardianintelligence.org/postflight`, with every Verself callback
    removed
  - Request user authorization during installation: disabled
  - Device Flow: disabled
  - Setup URL: `https://guardianintelligence.org/postflight`
  - Redirect on update: disabled
- Postflight user sign-in does not use this GitHub App. The Guardian Keycloak
  GitHub broker uses OAuth App client `Ov23li9wlYzzt3mcfJ7V`; Postflight uses
  GitHub App client `Iv23liDpxGOmBSQwSJ5i`. Keep the App out of user OAuth and
  retain the identity-keying ruling: `storeToken: false`, with no user-token
  persistence.
- Postflight's production webhook is active at
  `https://guardianintelligence.org/api/v1/github/webhooks`. It is the
  installation authority: the ingress routes to `postflight-controlplane`,
  the controlplane verifies the HMAC, and the signed `installation.created`
  event establishes organization linkage before the event is ledgered.
  Callback and setup redirects are not authority.
- Postflight's GitHub webhook secret must equal `webhookSecret` at OpenBao
  `kv/guardian/guardian-mgmt/postflight-runner/github-app`, which is projected
  as `GITHUB_WEBHOOK_SECRET` for the controlplane. GitHub does not reveal the
  current App-side value, so an explicit reconciliation re-enters the
  non-empty OpenBao value into GitHub; this is idempotent when already aligned
  and guarantees alignment when a Verself-era secret remains. Never rotate
  the OpenBao source merely to match an unknown App-side value.
- Callback and setup URLs remain empty for the other first-party Apps unless
  their baseline is explicitly amended.
- Postflight production and staging are distinct Apps. If the staging App has
  not been created, record `not-created` and do not create it during a sweep.
- `guardian-platform-app` does not receive Actions permission. GitHub App
  Packages write access does not replace the separately documented GHCR write
  exception.

## Repository baseline

Select **Only select repositories** for every first-party installation.

| App | Required repositories in `guardian-intelligence` |
| - | - |
| `Guardian Promotions` | `guardian` |
| `guardian-renovate` | `guardian` |
| `guardian-platform-app` | `guardian` |
| `Postflight by Guardian` | Only repositories whose default-branch workflow files contain a live Postflight `runs-on` label, plus an explicitly documented Postflight canary or benchmark repository |

For Postflight, recompute the set on every run. A repository name containing
`postflight` is not evidence by itself, and archived repositories are not
automatically retained. Use the default-branch workflow as the authority.
Production must not be installed in the staging canary organization; staging
must be installed only in that canary organization.

For a third-party App, GitHub controls the permission bundle. Narrow the
installation to **Only select repositories** and retain a repository only when
its default branch or an active operational record demonstrates use of that
App. Current examples are Blacksmith runner labels or actions in workflow
files. Agent integrations such as ChatGPT Codex Connector or Devin require an
explicit operator-approved repository list; do not infer organization-wide
access from their ability to work on many repositories.

## Step 1: Open the two inventories

Open these pages in separate tabs:

1. Owned Apps:
   `https://github.com/organizations/guardian-intelligence/settings/apps`
2. Installed Apps:
   `https://github.com/organizations/guardian-intelligence/settings/installations`

Create a ledger with these columns:

```text
app_name | owner | app_id | installation_id | owned_or_third_party |
repository_selection | repositories | repository_permissions |
organization_permissions | account_permissions | events |
pending_request | action | result
```

Copy every row from both pages into the ledger before changing anything. The
union, not the hard-coded App names in this manual, is the sweep set. Resolve
duplicate rows by App ID, but retain each distinct installation ID.

## Step 2: Reconcile each owned App definition

On the **Owned Apps** tab, process one App at a time:

1. Open **Edit** for the App and confirm the App ID and owner against the
   ledger.
2. On **General**, reconcile the complete baseline above. For Postflight,
   delete every Verself callback and set the Homepage, one callback, Setup,
   OAuth, Device Flow, redirect-on-update, webhook URL, webhook activity, and
   event settings exactly. For other Apps, confirm callback and setup URLs are
   empty. Do not touch private keys, client secrets, visibility, or ownership.
   Do not use callback or setup redirect success as installation-linkage
   evidence; the signed webhook is authoritative.
3. When the run explicitly includes Postflight webhook-secret reconciliation,
   first confirm that the OpenBao `webhookSecret` property exists and is
   non-empty. Transfer it directly into the GitHub **Webhook secret** field
   without displaying or persisting it. Because the App-side value is
   write-only, re-entering the OpenBao value is the comparison and repair.
   Confirm the `postflight-runner/github-app` ExternalSecret is Ready after the
   save and verify a signed delivery rather than printing either value.
4. Open **Permissions & events**.
5. Under **Repository permissions**, set every named permission to the exact
   baseline value. Set every other dropdown to **No access**.
6. Under **Organization permissions**, do the same.
7. Under **Account permissions**, set every permission, including Email
   addresses, to **No access**.
8. Under **Subscribe to events**, leave only the event in the baseline. For an
   App with no events, disable the webhook and ensure no event is selected.
9. Before saving, read the entire page again. Compare every non-**No access**
   row and every selected event with the baseline, not with the old state.
10. Select **Save changes** once.
11. Reload **Permissions & events** and record the rendered state. A toast is
    not acceptance evidence.

Removing access may take effect immediately. Adding access or an event can
create an installation permission request, so do not treat the App definition
save as the end of the sweep.

## Step 3: Reconcile each installation

Return to the **Installed Apps** tab and process every row, including
third-party Apps:

1. Select **Configure** and confirm the App name, owner, App ID, installation
   ID, and organization.
2. Read the full **Permissions** section into the ledger.
3. Under **Repository access**, select **Only select repositories**.
4. For a first-party App, select exactly the repository baseline. For a
   third-party App, apply the evidenced or operator-approved allowlist from the
   repository-baseline rules.
5. Select **Save**, reload the page, and record the rendered repository list.
6. If GitHub shows **Review request**, **New permissions requested**, or an
   equivalent banner, continue to Step 4 before leaving this App.
7. If no request is pending, compare the rendered installed permissions and
   events with the ledger target. Record `match` or the exact mismatch.

Do not use the repository's **Settings → GitHub Apps** page for this step: it
redirects to organization installation settings, where a save can affect every
repository selected in that installation.

## Step 4: Accept matching internal permission requests

Perform this step only for an App whose displayed owner is exactly
`guardian-intelligence`:

1. Open the request review from the installation page.
2. Expand every permission and event delta. Record the complete proposed
   post-acceptance state, not just the newly added rows.
3. Compare it with the first-party baseline. The comparison must include
   repository, organization, and account permissions plus webhook events.
4. If it matches exactly, select **Accept new permissions** or the equivalent
   final acceptance control.
5. Reload the installation page. Confirm the request banner is gone and the
   installed permission list now matches the baseline.
6. If it does not match exactly, do not accept it. Return to the owned App's
   **Permissions & events** page, correct the definition, save, and review the
   replacement request.

Never accept a third-party request in this manual. Record it as a blocker even
when the App is otherwise approved for use.

## Step 5: Stable second pass

After all Apps have been processed:

1. Reload the **Owned Apps** page and revisit every first-party
   **Permissions & events** page. Confirm the baseline again.
2. Reload the **Installed Apps** page. Confirm there is no pending internal
   request badge or banner.
3. Reopen every installation. Confirm its repository allowlist and installed
   permissions; do not rely on values recorded before acceptance.
4. Check that the owned-App and installed-App counts equal the ledger counts.
5. If the second pass changes anything, run another complete pass. Finish only
   after one pass produces no changes.

## Step 6: Read-only acceptance check

An organization owner with `read:org` can corroborate the final installation
state without changing it:

```sh
gh api --paginate orgs/guardian-intelligence/installations \
  --jq '.installations[] | {app_id, app_slug, id, repository_selection, permissions, events}'
```

For each first-party slug, corroborate the public App definition:

```sh
gh api apps/<app-slug> \
  --jq '{id,slug,owner:.owner.login,permissions,events,updated_at}'
```

The API does not replace the browser checks for the selected repository names
or a pending permission request banner.

Reference behavior:

- [Review and modify installed GitHub Apps](https://docs.github.com/en/apps/using-github-apps/reviewing-and-modifying-installed-github-apps)
- [Approve updated permissions for a GitHub App](https://docs.github.com/en/apps/using-github-apps/approving-updated-permissions-for-a-github-app)
- [Renovate GitHub App permissions](https://docs.renovatebot.com/modules/platform/github/#running-as-a-github-app)

## Output record

Return the completed ledger plus this summary:

```text
organization=guardian-intelligence
owned_apps=<count>
installed_apps=<count>
first_party_matched=<count>/<count>
third_party_reviewed=<count>/<count>
repositories_narrowed=<count>
internal_requests_accepted=<count>
internal_requests_remaining=<count>
third_party_requests_pending=<count>
unexplained_drift=<count>
second_pass_changes=0
result=<pass|blocked>
blockers=<none|exact list>
```

`result=pass` requires zero internal requests remaining, zero unexplained
drift, and a stable second pass. A third-party request remains a blocker until
its new permission bundle is separately approved and this manual is updated.
