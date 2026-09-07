# Guardian CI on Postflight

The target is all Guardian CI executing on Postflight Turbo VMs, with
GitHub retaining the workflow DAG, checks, retries, and native live logs.
The canonical label is `postflight-4vcpu-ubuntu24-turbo`. It does not
require SEV-SNP. A static self-hosted runner carrying a different label is
bootstrap capacity, not evidence that Postflight's control plane and hostd
executed a job.

## Inventory and current cutover boundary

Audit baseline: 2026-09-06, repository revision
`2b9c745c405ea9de6386db448099c6dd9c79b0f4`. Live settings below are a
snapshot, not a completion claim.

| Workflow | Jobs | Trigger and constraint |
| --- | --- | --- |
| `build-and-test.yml` | `build-and-test` | PR, main push, dispatch; the only required merge check; builds/tests `//...` and verifies changed tool pins |
| `images.yml` | `classify`, `publish` matrix | Main build-input changes; up to 15 image publications; Docker registry login and Bazel |
| `images-lock-sign.yml` | `sign` | Main manifest changes; GHCR, cosign, ORAS, GitHub OIDC |
| `postflight-cli-image.yml` | `edge` | Main CLI changes; Rust/zig cross-build, native smoke test, GHCR, cosign and SBOM |
| `postflight-cli-release.yml` | `release` | Main channel-pin changes; verifies signed OCI, cuts releases and updates Homebrew with scoped App tokens |
| `postflight-cli-publish-npm.yml` | `publish` | Stable release; five packages, tokenless npm trusted publishing |
| `postflight-cli-publish-crates.yml` | `publish` | Stable release; exact release-tag sources, Rust, crates.io trusted publishing |

All eight declared job templates used `ubuntu-latest` at the audit
baseline. PR run [34032744882](https://github.com/guardian-intelligence/guardian/actions/runs/34032744882)
completed successfully on a GitHub-hosted runner in 5m13s. The repository
runner API exposed only `guardian-release-0`, online with
`self-hosted`, `Linux`, `X64`, and `guardian-release` labels.

The Postflight GitHub App exists: App `3370540`, installation
`123769944`, selected repositories, `workflow_job` subscription. Confirm
the installation includes `guardian`, and that its organization runner
group allows this public repository. The App requires Actions read,
metadata read, pull requests write, and organization self-hosted runners
write; its OpenBao custody is documented in [GitHub Apps](github-apps.md).

The live `main-protection` ruleset requires `build-and-test`; keep that
job key and its single-job workflow intact. Its live ruleset did not yet
include the review requirements already declared in `guardian-github`.
The OpenTofu CronJob has no `MODE` override and therefore plans only.
Inspect the complete root plan before enabling apply; a policy import
must not hide unrelated accumulated changes.

## Bootstrap and executable canary

The existing merge-gate workflow has an explicit dispatch-only canary:

```sh
gh workflow run build-and-test.yml --ref main -f runner=postflight
```

Default dispatch, pull requests, and pushes remain on GitHub-hosted
compute until the cutover gates below pass. There is no second required
job, extra PR workflow, or secret available to the canary. The Postflight
path uses the reviewed, commit-pinned `actions/checkout` implementation in
this repository, preserves untracked build state, and runs the actual
Bazel build and test graph. It skips GitHub's `actions/cache` restore:
`~/.cache/bazel-ci` is persisted by the Postflight tool volume instead.

Bootstrap in this order:

1. Merge the checkout allowlist declaration and its OpenTofu ownership
   separately from workflows that first reference that action. Inspect the
   root plan and reconcile the setting; confirm the exact pin through
   `gh api repos/guardian-intelligence/guardian/actions/permissions/selected-actions`.
   A disallowed action can reject a whole job before any step runs, even
   when an unrelated execution path was intended.
2. Deploy the Turbo runner class and the host/guest artifacts through
   their declared deployment paths while the current publishers still
   work. Verify hostd inventory reports real KVM/ZFS capacity for the
   canonical class and the App receives signed webhooks for `guardian`.
3. Merge the canary dispatch path. The first canary demand may create the
   organization's class pool; verify JIT registration, an online listener,
   GitHub assignment, guest binding, and native step output. Do not relabel
   an ordinary runner with the canonical label to satisfy this check.
4. Run the dispatch twice on the same reviewed main SHA, waiting for the
   first assignment's seal and the scope pointer to commit before the
   second dispatch. Record each run/job URL, execution ID, host ID, VM
   incarnation, generation ID, queue time, and build/test time.
5. Only after the proof below, move the regular build gate, image
   classifier/publishers, signing lanes, and release lanes to Turbo in
   reviewed changes. Preserve workflow filenames and OIDC identities.
   Size capacity for the image matrix plus the security jobs; four vCPUs
   per job is a resource reservation, not permission to oversubscribe.

The dispatch prints execution/attempt IDs, guest boot ID, mount sources,
and pre-build Bazel cache size. These are correlation evidence, not
snapshot proof or a speed claim. The build/test logs show actual Bazel
cache use and failures. Keep bootstrap publishers available until a
Postflight-produced control-plane/host update has itself converged.

## Required evidence for caching, snapshots, and logs

Disk warmth is proven across two different VM incarnations. The first
successful trusted main assignment must produce a sealed, committed
workspace/tool generation. Verify
`runner_job_assignments.seal_generation`,
`workspace_scopes.current_generation_id`, and the referenced
`workspace_generations` state; correlate hostd's
`snapshot_seal_completed` event. The second assignment must name that
committed generation in `source_generation`, materialize its ZFS clone,
mount it in the guest, and reuse actual Bazel action/repository cache
entries. A marker file or second build inside the same VM proves neither
cross-VM isolation nor snapshot restoration.

VM warm boot is a separate claim. A reusable preboot template must have
been captured before any GitHub JIT configuration, runner registration,
job credential, or customer workspace was introduced. Record the pinned
guest/template digest, restore event, new VM incarnation, and fresh
registration for each restored guest. Job and runner credential state
must not enter the template. A normal cold QEMU start is not snapshot
restoration. Host or image incompatibility must produce a cold start or
a named failure, never silently reuse an incompatible image.

Arbitrary build-process CRIU persistence is currently disabled in
`guestd` and hostd following security hardening. Do not re-enable it to
make a canary pass, and do not call disk snapshots restored process
memory. Any future process-capsule work needs its own confidentiality,
credential-exclusion, and restore-compatibility proof.

For log streaming, watch the GitHub job while it runs and observe real
build output before completion. Correlate the job ID, runner assignment,
and hostd/guestd transitions in telemetry. Completion requires the same
job's terminal conclusion to agree between GitHub and the control plane,
followed by guest destruction/refill and credential removal. Queued jobs,
webhook acceptance, registration, and downloaded final logs alone do not
prove live streaming or lifecycle completion.

Exercise a failed build, cancellation, and a cold-cache miss before
switching the required gate. Confirm PR assignments cannot promote cache
generations into trusted branch scope; only successful permitted branch
runs may advance their own lineage. Preserve the current PR/no-secrets
boundary in [.github/workflows/AGENTS.md](../.github/workflows/AGENTS.md).

## CI outside checked-in workflow files

The GitHub workflow inventory includes dynamic workflows that a
`runs-on` search cannot find:

- **CodeQL default setup:** enabled for Actions, Python, and Rust, with
  weekly scheduling and `runner_type=standard` at audit time. PR run
  [34032742763](https://github.com/guardian-intelligence/guardian/actions/runs/34032742763)
  ran all three analyses on `ubuntu-latest`. Configure default setup to
  the canonical Postflight custom label after that pool is healthy;
  GitHub supports custom labels on self-hosted CodeQL runners. Re-run all
  three languages and inspect their actual runner labels. Preserve
  security coverage; no duplicate workflow file is needed. See
  [GitHub's default-setup runner configuration](https://docs.github.com/en/code-security/how-tos/find-and-fix-code-vulnerabilities/configure-code-scanning/configure-code-scanning).
- **Dependency Graph:** `dynamic/dependabot/update-graph` appears in the
  API. Its latest observed run,
  [32689496260](https://github.com/guardian-intelligence/guardian/actions/runs/32689496260),
  executed `update-go_modules-graph` on GitHub-hosted compute on August 24.
  Inspect repository dependency-submission settings separately. Automatic
  dependency submission supports the
  `dependency-submission` self-hosted label, with hosted fallback when
  no suitable runner is available. Postflight currently resolves only
  `postflight-*` labels, so adding the generic label alone will not create
  demand or prove supported assignment. Add an explicit supported mapping
  and pool labels, then exercise a manifest change and verify runner
  identity; distinguish Dependabot graph jobs from automatic submission.
  See [GitHub's dependency-submission documentation](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/secure-your-dependencies/submit-dependencies-automatically).
  GitHub's documented Dependabot self-hosted route excludes public
  repositories such as Guardian. It must not be assumed to migrate these
  graph jobs. See [Dependabot's runner restrictions](https://docs.github.com/en/code-security/concepts/supply-chain-security/dependabot-on-actions).
- **Historical workflow records:** `checks.yml` and
  `cli-release-debug.yml` still appear as active API workflow records but
  are absent from the current git tree. Their listing alone is not proof
  of scheduled compute. Check run history before retiring any record.

The pinned GitHub Terraform provider 6.13.0 does not expose CodeQL
default-setup or automatic dependency-submission configuration resources.
These settings need declared reconciliation before an all-compute
completion claim; removing scans is not a migration.

### Dependency artifact replacement

The prepared cutover uses an additional trusted-main artifact-publisher
job in the existing `images.yml`, with `contents: write` only for that
job's dependency snapshot API upload. It scans `git archive HEAD` with
Syft 1.51.1, then [.github/scripts/dependency_snapshot.py](../.github/scripts/dependency_snapshot.py)
binds the result to the exact SHA, main ref, run/attempt, and a stable
correlator. Scanning a clean archive excludes restored build products and
cache contents. The command itself does not publish or read credentials.

The helper requires all five current package manifest paths, rejects
empty or missing manifests and unresolved graph edges, and leaves
GitHub's static Actions analysis untouched. Syft 1.51.1's GitHub exporter
reverses dependency-of edges and labels every dependency direct; the
helper corrects the direction and omits unsupported directness claims.
Its detector version is exact so a future exporter change requires a
review. See the [pinned exporter source](https://github.com/anchore/syft/blob/v1.51.1/syft/format/github/internal/model/model.go).

A local scan of the audited main revision reproduced all 125 unique
PyPI versions, 112 Cargo versions, and 543 concrete npm versions in the
live GitHub SBOM. All previously reported Go URLs matched after
normalizing GitHub's casing and URL escaping. The remaining 43 static npm
records used the literal version `catalog:`; the lockfile supplied their
actual versions. Duplicate records from removed Python paths explain
why raw SBOM package totals are not the coverage gate.

Before stopping any generated GitHub graph job, submit a real snapshot,
verify its accepted snapshot ID and reread graph/security coverage,
including transitive versions. User submissions take precedence over
generated snapshots for the same manifest, so an incomplete submission
would reduce coverage even with the old jobs still enabled. See the
[dependency snapshot API and precedence](https://docs.github.com/en/rest/dependency-graph/dependency-submission).
Neither the helper nor the prepared publisher disables the dependency
graph, Dependabot alerts, or generated jobs. A supported way to stop
public-repository Dependabot graph compute independently of the graph
remains to be verified; setting automatic submission to disabled is not
proof those graph jobs stop.

## Release-lane compatibility and npm decision

Keep the existing cosign certificate subjects, issuer, build verification,
SBOMs, release gates, and workflow filenames. Self-hosted Actions jobs can
still receive GitHub workflow tokens and OIDC; test the signed consumer
path on Turbo before moving the release publishers.

The current Postflight checkout contract binds the requested full ref to
`GITHUB_SHA`. The npm lane intentionally checks out today's default branch
while processing an older release event. That is not a mechanical action
swap: retain its explicit stock checkout until a separately authorized
ref-resolution contract supports this source selection. Its packaging
and signature checks still run on the chosen execution runner.

As verified on 2026-09-06, npm trusted publishing explicitly excludes
self-hosted runners. The existing tokenless npm workflow cannot simply
move to Turbo. See [npm's trusted-publishing limitations](https://docs.npmjs.com/trusted-publishers/).
To meet the all-compute goal, the owner must choose a scoped publishing
credential for the five existing Postflight packages: no organization
administration, a short expiry, and an egress IP restriction once the
worker's stable address is verified. Unattended publication may require
the token's explicit 2FA-bypass option; this is an owner decision.
Store it through the approved secret custody and expose it only to the
stable-release publisher. Keep verification of the original signed
release bytes. See [npm's granular-token controls](https://docs.npmjs.com/creating-and-viewing-access-tokens/).

The prepared publisher expects `POSTFLIGHT_NPM_TOKEN` and fails clearly
when it is absent. It passes the token only to the publish step and uses
an ephemeral `/tmp` npm config; registry authentication in the image
publishers likewise uses `/tmp`, outside the ZFS tool volume. Stock
checkout in the release lanes explicitly disables persisted credentials.

npm also documents a cloud-hosted-runner limitation for provenance.
The candidate retains `--provenance`, the GitHub OIDC grant, and every
existing signed-binary verification; a scoped token alone therefore does
not prove the registry will accept the candidate. Do not remove
provenance or misstate the builder identity to get a green run. Verify
registry acceptance or resolve the publishing design with the owner.
See [npm's provenance limitations](https://docs.npmjs.com/generating-provenance-statements/).

No npm token, publishing permission, signing relaxation, or permanent
hosted exception is introduced by the canary preparation. A hosted npm
publisher would remain an explicit incomplete cutover, not all CI on
Postflight. Verify crates.io's OIDC exchange separately on the intended
runner before changing that lane; npm's restriction is not evidence of
the crates.io policy.
