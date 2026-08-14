# Operator workstation and Talos

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
- The apiserver firewall admits only the operations VPS (`operatorSubnets`
  in `src/infrastructure/talm/values.yaml`). Any other workstation (e.g. a
  macOS dev machine) runs `tools/ops/mgmt-tunnel install` once: launchd then
  keeps an SSH tunnel on `127.0.0.1:16443` through the VPS, and both
  `aspect infra auth` and kubectl use it automatically — guardian_auth probes
  the loopback port before the direct endpoint, so no flags are needed on
  either kind of machine.
- The kube API is reachable via the default `~/.kube/config`, whose only
  standing identity is the `read` persona (the `platform-agent` OIDC context,
  set up with `aspect infra auth`): cluster-wide read plus port-forward, and the
  only rung that refreshes unattended. Product-specific operations derive
  short-lived capability identities (`aspect mythra ...` is the first); repair
  verbs outside those narrow roles come from
  `--persona=write-basic` (non-root secret writes only) and tenant-root secret
  writes or emergencies from `--persona=write-all`; neither write persona
  holds `offline_access`, so each costs an operator device approval and expires
  with its Keycloak session. There is no standing admin kubeconfig anywhere on
  disk; breakglass x509 is minted on demand with
  `aspect infra auth --persona=root --reason "<why>"` and dies with its short
  cert lifetime. The ladder lives in
  `src/infrastructure/base/cozystack-identities/platform-admins.yaml`.
- Workstation hygiene is a launchd agent, not a habit: `tools/ops/workspace-watch
  install` fast-forwards the primary checkout whenever that is a no-op for local
  work, removes worktrees whose branch is already in `origin/main`, and keeps the
  `read` persona's offline token from idling out of its 30-day window. Locked,
  dirty, in-use, and recently touched worktrees are never removed —
  `git worktree lock` is the opt-out other agents get for free.
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

Every node arms its AMD SP5100 TCO chipset watchdog (`/dev/watchdog0`, 1m timeout) via a `WatchdogTimerConfig` document

<scratchpad>
* Cluster autorotates CA every 90 days
* The three management nodes boot factory Sidero-signed Talos UKIs with UEFI
  Secure Boot enabled. Talos encrypts STATE, EPHEMERAL, and the LINSTOR raw
  volume with TPM-backed LUKS2; customer and business PVCs add Cozystack-native
  LINSTOR LUKS. The control and audit evidence are in
  `docs/management-cluster-trusted-boot-and-storage.md`.
* Automated etcd snapshots to R2
</scratchpad>

## Drills

* Drills are not part of normal development — run them when asked on one node at a time by explicit node IP, wait for the node and public edge to recover, document that node's outage window, then move to the next. A node whose loss breaches 60 seconds of public-edge disruption is load-bearing and must be fixed before continuing.
