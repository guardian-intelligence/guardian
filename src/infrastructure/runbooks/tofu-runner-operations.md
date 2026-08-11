# tofu-runner operations: pause, apply-now, flip, and break-glass

The bootstrap OpenTofu roots reconcile in-cluster (docs/tofu-gitops-design.md):
one CronJob per root runs the tofu-runner on the root's schedule, fetches the
Flux source artifact, and runs `tofu plan`. A root in plan mode holds; a root
in apply mode applies a non-empty plan. Each tick is a fresh Job with no
long-lived process, and R2 conditional-write locking (`use_lockfile`) is the
mutex.

Four operator moves exist — everything else is a PR.

## Read a plan or a result

The plan is the Job's pod log:

```sh
kubectl logs -n tofu-system job/<job-name>            # a specific run
kubectl logs -n tofu-system -l guardian.dev/tofu-root=<root> --tail=-1
```

A failed Job means a tofu error, a failed apply, or — on the token minter —
a plan with changes (its `PAGE_ON_DRIFT` makes drift a failure). The
`tofu-runner-health` VMRule pages on these.

## Read a root's outputs

Roots do not relay outputs anywhere: an output (a tunnel token, a webhook
signing secret) lives only in the root's encrypted R2 state. To read one —
including the relay ceremony that seeds OpenBao after an output-bearing
apply — assemble the break-glass credentials below and run
`tofu output <name>` in the root. Treat any output-bearing apply as
unfinished until its consumers' OpenBao copies are re-seeded.

## Apply now (don't wait for the next tick)

A merge applies on the root's next scheduled tick. To run a root immediately
— after merging a change to an apply-mode root, or to re-check drift on
demand — spawn a one-off Job from its CronJob:

```sh
kubectl create job -n tofu-system --from=cronjob/tofu-<root> tofu-<root>-manual-$(date +%s)
```

It runs the identical pod. `concurrencyPolicy: Forbid` does not apply to
manually created Jobs, so avoid firing one while a scheduled Job is mid-run
— `use_lockfile` will still serialize them, but the second waits on the lock.

## Pause a root (incident-time manual changes)

An apply-mode root will revert a manual provider-console change on its next
reconcile. Before making one — the canonical case is an incident-time
Cloudflare change on `guardian-mgmt-dns` — suspend that root's CronJob:

```sh
kubectl patch cronjob/tofu-<root> -n tofu-system --type=merge -p '{"spec":{"suspend":true}}'
```

Suspension stops future Jobs only; an already-running Job finishes. When the
incident closes, decide the manual change's fate before resuming: codify it
(PR the same change into the root, merge, then resume — the next plan is a
no-op) or revert it (resume and let the apply restore the declared state).
Resuming is the same patch with `false`.

When the cluster is down hard enough that the runner CronJobs cannot fire,
manual provider changes stick on their own — nothing is reconciling. That
failure mode needs no pause; it needs the recovery runbooks.

## Flip a root from plan to apply

Roots start in plan mode (the runner defaults to plan; the CronJob sets no
`MODE`). The flip to apply, once a root's soak shows clean, stable plans, is
its own reviewed PR that adds one env var to the root's CronJob:

```yaml
              env:
                - name: MODE
                  value: apply
```

Follow the ruled order in docs/tofu-gitops-design.md (sandbox first, the
metal last). Reverting a flip is deleting that env — the root falls back to
plan-and-hold.

## Break-glass workstation apply

For when a root must change while the CronJobs cannot run it (a runner
regression, cluster degraded but provider reachable). This is the only
sanctioned workstation apply, and it does not involve the custody bundle. The
workstation runs the same OpenTofu the image ships (the multitool tofu pin is
bound to the image's by TestTofuRunnerTracksMultitoolPin), so a hand apply
and an in-cluster apply plan identically against the same state.

1. Suspend the root's CronJob (above), so a scheduled Job and the
   workstation cannot contend. `use_lockfile` backstops the race regardless.
2. Assemble credentials:
   - `TF_ENCRYPTION`: the state-encryption passphrase from the operator
     vault (`tofu-state-encryption`), as
     `{"key_provider":{"pbkdf2":{"state":{"passphrase":"<value>"}}}}`.
   - R2 backend env (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`,
     `AWS_ENDPOINT_URL_S3`): re-mint a `guardian-vault`-scoped R2 token
     from the Cloudflare dashboard (the account login lives in the
     operator vault); access key = token id, secret = SHA-256 hex of the
     token value.
   - The root's provider credential: re-issue from its owning console
     (Cloudflare token via the dashboard, Stripe sandbox key, GitHub
     fine-grained PAT, Latitude token). OpenBao's copies are deliberately
     unreadable; consoles are the break-glass source.
3. Plan/apply through the multitool tofu pin:

   ```sh
   bazelisk run @multitool//tools/tofu:workspace_root -- \
     -chdir=src/infrastructure/bootstrap/<root> plan -input=false \
     -var-file=src/infrastructure/bootstrap/backend.tfvars
   ```

4. Afterwards: if the emergency change isn't already declared in the root,
   PR it now — resuming with undeclared changes hands an apply-mode root a
   revert. Then resume the CronJob and confirm its next plan is a no-op.
