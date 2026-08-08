# tofu-controller operations: pause and break-glass

The bootstrap OpenTofu roots reconcile in-cluster
(docs/tofu-gitops-design.md): merged PRs apply automatically, and drift
detection re-plans on each root's interval. Two operator moves exist, and
only two — everything else is a PR.

## Pause a root (incident-time manual changes)

Drift detection will revert a manual provider-console change on the next
reconcile. Before making one — the canonical case is an incident-time
Cloudflare change on `guardian-mgmt-dns` — suspend that root:

```sh
kubectl patch terraform/<root> -n tofu-system --type=merge -p '{"spec":{"suspend":true}}'
```

Suspension stops subsequent executions only; an already-running apply
finishes. When the incident closes, decide the manual change's fate before
resuming: codify it (PR the same change into the root, merge, then resume —
the next plan is a no-op) or revert it (resume and let drift detection
restore the declared state). Resuming is the same patch with `false`.

When the cluster is down hard enough that the controller is down too,
manual Cloudflare changes stick on their own — the reconciler that would
revert them isn't running. That failure mode needs no pause; it needs the
recovery runbooks.

## Break-glass workstation apply

For when a root must change while the controller cannot run it (controller
regression, cluster degraded but provider reachable). This is the only
sanctioned workstation apply, and it does not involve the custody bundle:

1. Suspend the root (above), so the controller and the workstation cannot
   contend. State locking (`use_lockfile`) backstops the race regardless.
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
3. `aspect infra tofu-init --root <root>`, then plan/apply through the
   multitool tofu pin:

   ```sh
   bazelisk run @multitool//tools/tofu:workspace_root -- \
     -chdir=src/infrastructure/bootstrap/<root> plan -input=false \
     -var-file=src/infrastructure/bootstrap/backend.tfvars
   ```

4. Afterwards: if the emergency change isn't already declared in the root,
   PR it now — resuming with undeclared changes hands drift detection a
   revert. Then resume the root and confirm its next plan is a no-op.
