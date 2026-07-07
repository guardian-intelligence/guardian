# Tribal knowledge

## Cloudflare bootstrap credentials

Guardian uses three separate Cloudflare credentials for the ASH management
cluster edge. Keep the runtime controller token narrow; keep provisioner and
state credentials outside the cluster so a wiped cluster can still be rebuilt.

| Credential | Consumer | Durable home | Scope |
| - | - | - | - |
| `cloudflare_external_dns_api_token` | ExternalDNS in `external-dns` | OpenBao path `kv/guardian/guardian-mgmt/tenant-guardian/dns/external-dns`, projected by External Secrets Operator | Zone `guardianintelligence.org`: `Zone Read`, `DNS Read`, `DNS Write` |
| `cloudflare_dns_lb_provisioner_api_token` | OpenTofu root `src/infrastructure/bootstrap/guardian-mgmt-dns` during edge bootstrap or recovery | Off-cluster break-glass or CI secret store; injected only into the apply environment | Account: `Load Balancing: Monitors and Pools Read`, `Load Balancing: Monitors and Pools Write`; Zone `guardianintelligence.org`: `Zone Read`, `Load Balancers Read`, `Load Balancers Write` |
| `cloudflare_r2_access_key_id` and `cloudflare_r2_secret_access_key` | OpenTofu S3-compatible backend for repo-owned state | Off-cluster break-glass or CI secret store; injected only into the apply environment | R2 `Object Read & Write`, scoped to the OpenTofu state bucket `guardian-vault` |

User must pay $10/mo to enable CloudFlare LB with 3 endpoints (1 for each ingress node). This is not enabled by default.

## Talos access from the operator workstation

- The live talosconfig is `src/infrastructure/talm/talosconfig` (gitignored;
  its encrypted twin `talosconfig.encrypted` is committed — decryption is
  covered by the cold-boot runbook). **Do not trust `~/.talos/config`**: it
  holds endpoints of a previous cluster generation and every one of them
  times out. If `talosctl` hangs on port 50000, you are almost certainly
  using the stale global config.
- Current node public IPs are recorded in the `# talm:` modeline on the
  first line of each `src/infrastructure/talm/nodes/*.yaml` — that is the
  source of truth and it changes on reimage. Port 50000 is open on those
  IPs from the operator workstation.
- The kube API is reachable via the kubeconfig kept off-repo at
  `~/guardian-custody/kubeconfig-public` on the operator workstation.
- Machine config applies are per-node, base plus overlay:
  `talm apply -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml`.

## Regenerating node configs (`talm template -I`)

The install-disk regression is fixed (`talos.install.disk_pin` emits
`diskSelector.serial`; a bare `/dev/nvmeXn1` can point at a different
physical disk on the next boot). Regen output is still not byte-convergent:
talm's re-marshal drops quotes and reorders map keys, discovered-disk
comments follow boot enumeration order, and live network state (hostname,
MTU, VLANConfig) echoes into the base files that the `*-overlay.yaml` files
own. Review regen diffs hunk-by-hunk before committing them; never commit a
`diskSelector` → `disk:` change.

## Hardware watchdog (armed on all nodes since PR #338)

Every node arms its AMD SP5100 TCO chipset watchdog (`/dev/watchdog0`,
1m timeout) via a `WatchdogTimerConfig` document; a hard kernel hang
reboots the node with no operator action (measured: 2m22s crash → Ready,
OpenBao replica auto-unsealed at +4m). Verify with
`talosctl get watchdogtimerstatuses` (sysfs `state` must read `active`).
To re-run the positive test: set `kernel.panic=0` first (Talos defaults it
to 10, and a panic self-reboot contaminates the result), then
`echo c > /proc/sysrq-trigger` from a privileged debug pod; afterwards
`/sys/class/watchdog/watchdog0/bootstatus` must read 32 (WDIOF_CARDRESET —
the chipset recording that it caused the last reset). Watchdog recoveries
are silent by design; the dead-man's-switch alerting work is what makes
them observable.

## Promotion enforcement lives partly in repo settings (not Git)

The Kargo promotion bot is untrusted by construction only while GitHub
enforces the checks: branch protection on `main` requiring the `build` and
`site-gate` contexts, plus repo-level allow-auto-merge. Those settings are
not represented in Git — re-assert them if the repo is ever recreated or
protection is accidentally dropped:

```sh
gh api -X PATCH repos/guardian-intelligence/guardian \
  -F allow_auto_merge=true
gh api -X PUT repos/guardian-intelligence/guardian/branches/main/protection \
  --input - <<'JSON'
{
  "required_status_checks": {"strict": false, "contexts": ["build", "site-gate"]},
  "enforce_admins": false,
  "required_pull_request_reviews": null,
  "restrictions": null
}
JSON
```

Both checks run on every PR (`site-gate` classifies the diff itself and
exits fast when nothing relevant changed — required checks cannot be
path-filtered without hanging unrelated PRs). The `guardian-promotions`
GitHub App (private key in operator custody; also in the repo Actions
secrets for promotion-automerge) must stay scoped to Contents + Pull
requests read/write.

## Watching the cluster converge (`aspect infra watch`)

When you push a change, watch whether it actually reconciles instead of
guessing. Two read-only views:

```sh
aspect infra watch                       # live Flux status with repo-pinned kubectl
aspect infra watch --mode=convergence    # ntfy stream: Flux convergence alerts only
aspect infra watch --mode=stream         # ntfy stream: all alerts, no cluster access
```

Use the default live status view to babysit a PR: it reads Kustomization and HelmRelease
Ready conditions every few seconds and prints exactly the ones that are not
Ready, with the reason and message (`BuildFailed`, `HealthCheckFailed`,
`UpgradeFailed`, ...). A bad manifest shows up in seconds. The ntfy stream is
pager-oriented: many rules intentionally hold for ~15m before firing, so it
tells you about *sustained* failure, not a fresh push.

The stream view needs nothing but network: the cluster already publishes
every alert to the `guardian-operations-fable` ntfy topic (via
`alert-relay`), and that topic is world-subscribable, so from any machine:

```sh
curl -s "https://ntfy.sh/guardian-operations-fable/json?since=15m"
```

is the zero-dependency equivalent of the stream view. Override the topic
with `NTFY_TOPIC` / `NTFY_BASE` if the sink ever moves. (If the topic is
later locked down, subscribers will need a token — the relay is the single
place that knows the URL today.)
