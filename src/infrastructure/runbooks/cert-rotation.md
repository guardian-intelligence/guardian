# Certificate rotation

How Guardian rotates the cluster's certificate authorities and the admin
credentials minted from them. Two independent things live here and must not
be conflated:

- **Admin kubeconfig lifetime** — the `CN=admin,O=system:masters` client
  cert minted by `talm`/`talosctl kubeconfig`. `system:masters` is hardwired
  to cluster-admin and **cannot be revoked through RBAC**, so a minted admin
  kubeconfig is a bearer credential that is valid until it expires. We keep
  that window short (`cluster.adminKubeconfig.certLifetime`, 8h) so expiry
  *is* the revocation. This needs no CA rotation — it is a machine-config
  field, applied like any other.
- **CA rotation** — replacing a root CA itself. This is the only way to
  invalidate certificates already issued (leaked `secrets.yaml`, or simply
  retiring the population of long-lived admin certs that predate the short
  lifetime). It is a live, minutes-long cluster operation with a rolling
  restart of the control plane. Rehearse it; do not first run it under fire.

Trust root for everything below is the custody bundle
(`talm/secrets.yaml` + `talm/talosconfig`), which is only ever plaintext on
tmpfs between an `aspect infra custody --action restore` and `--action
wipe` — see `custody.md`. The leak-response triage that decides *which* of
these to run lives in `custody.md` ("Leak response"); this runbook is the
executable procedure each one points at.

## Admin cert lifetime (no rotation)

`cluster.adminKubeconfig.certLifetime` in `talm/values.yaml` sets how long a
freshly minted admin kubeconfig lives. It is emitted into all three
control-plane node configs. To change it: edit the template value, re-render
is unsafe (see the `talm template -I` warning in `TRIBAL_KNOWLEDGE.md`), so
hand-edit `values.yaml` and the three `nodes/*.yaml` together, then apply per
node:

```sh
# from a restored bundle assembled onto a tmpfs mint root (see below)
talm apply -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml \
  --root "$MINT" --talosconfig "$MINT/talosconfig"
```

The field takes effect on the next mint; existing kubeconfigs keep their
original `notAfter`. Verify:

```sh
talm kubeconfig --root "$MINT" --talosconfig "$MINT/talosconfig" \
  --nodes <ip> --endpoints <ip> --merge=false --force
grep client-certificate-data "$MINT/kubeconfig" | awk '{print $2}' \
  | base64 -d | openssl x509 -noout -dates   # notAfter ~8h out
```

## Kubernetes CA rotation (the drill)

Rotates **only** the Kubernetes API issuing CA — the certificate that signs
the API server and every client cert (admin, kubelet-client, controllers).
It leaves the Talos API CA, etcd CA, and OpenBao seal untouched. Use
`--talos=false`; the Talos CA is a separate, heavier rotation with its own
`talosctl` reconnect dance.

This was first executed as a drill on 2026-07-07 (see the log). Every gotcha
below cost a real minute that night.

### 0. Prerequisites

- A restored custody bundle. `aspect infra custody --action restore --yes`
  (with `RESTIC_PASSWORD_FILE` set, or it prompts).
- The pinned `talosctl` and `talm` from the repo toolchain, and the pinned
  `kubectl`. Build them in the current worktree — Bazel output bases are
  per-worktree.
- A quiet-ish window. The control plane rolling-restarts; app traffic on
  `*.guardianintelligence.org` (Cloudflare → nodes) is unaffected, but
  in-cluster controllers briefly lose their API connection and crashloop
  until they re-read the new root CA (this is expected — see step 4).
- Post a heads-up to `ntfy.sh/guardian-operations-fable`.

### 1. Assemble a tmpfs mint root

`talm` needs the tracked chart (templates + node configs) *and* the bundle's
`secrets.yaml`/`talm.key`/`talosconfig` in one directory. Never assemble this
in the repo tree — the secret-scan hook will (correctly) block you, and it is
the exact residue custody exists to prevent. Build it on tmpfs:

```sh
MINT=/dev/shm/guardian-talm-mint
rm -rf "$MINT" && mkdir -m 700 "$MINT"
cp -a src/infrastructure/talm/. "$MINT/"
cp /dev/shm/guardian-custody/talm/{secrets.yaml,talm.key,talosconfig} "$MINT/"
```

(`aspect infra auth --platform-admin` does exactly this internally via
`_assemble_mint_root`; the manual form is here because rotation is not yet
wrapped in an aspect task.)

### 2. Dry run

`rotate-ca` dry-runs by default. Point it at a **node's public IP**, not the
VIP:

```sh
TALOSCTL=<pinned talosctl>
"$TALOSCTL" --talosconfig "$MINT/talosconfig" rotate-ca --talos=false \
  -e 206.223.228.101,45.250.254.119,206.223.228.87 \
  -n 206.223.228.101,45.250.254.119,206.223.228.87 \
  --control-plane-nodes 206.223.228.101,45.250.254.119,206.223.228.87 \
  --k8s-endpoint 206.223.228.101:6443 \
  --with-docs=false --with-examples=false \
  -o "$MINT/talosconfig.rotated"
```

Two flags that are not optional here, each learned the hard way:

- **`--k8s-endpoint <nodeIP>:6443`.** Without it, `rotate-ca` builds its
  Kubernetes client from the bundle kubeconfig's server — the VIP
  `10.8.0.250:6443`, which is on the private VLAN and **times out from the
  operator VPS** (`dial tcp 10.8.0.250:6443: i/o timeout`). It hangs at
  "Verifying connectivity with existing PKI". Pin it to a routable node IP.
- **`-n`/`-e` with node IPs.** `--control-plane-nodes` alone satisfies the
  topology walk but the command still needs `--nodes`/`--endpoints` for its
  Talos API calls, or it errors `nodes are not set for the command`.

A clean dry run prints the current and new CA, then walks the four phases
(add-accepted → make-issuing → verify → remove-old) as `skipped (dry-run)`.

### 3. Execute

Same command, add `--dry-run=false`. Redact the key material from any saved
output (`| grep -v '^  key:\|^  crt:'`). The phases now run for real:

```
> Adding new Kubernetes CA as accepted...        # both CAs trusted
> Making new Kubernetes CA the issuing CA...      # new CA signs; old still accepted
> Verifying connectivity with new PKI... - OK
> Removing old Kubernetes CA from the accepted CAs...   # old certs die HERE
> Verifying connectivity with new PKI... - OK
```

The moment "Removing old" completes, every certificate signed by the old CA —
including all the long-lived admin kubeconfigs — is refused. That is the
point of the drill.

### 4. Expected fallout during convergence

The control plane rolls one static-pod set at a time. The drill produced two
distinct failures, **both of which needed hands-on recovery** — do not expect
the cluster to converge on its own. One is a fleet-wide stale-credential
problem fixed by recreating pods; the other is a single wedged node fixed by
a reboot. Budget ~15 minutes of active work.

Needs intervention #1 — the big one (this dominated recovery time):

- **Controllers cluster-wide crashloop with
  `x509: certificate signed by unknown authority ... "kubernetes"`** when
  dialing the apiserver (`localhost:7445` KubePrism or `10.96.0.1:443`).
  Root cause, confirmed in the drill: the `kube-root-ca.crt` ConfigMap in
  every namespace IS updated to the new CA (verify:
  `kubectl -n <ns> get cm kube-root-ca.crt -o jsonpath='{.data.ca\.crt}' | openssl x509 -noout -fingerprint -sha256`),
  and **brand-new pods come up fine** — but the kubelet does **not** refresh
  the *already-projected* `/var/run/secrets/kubernetes.io/serviceaccount/ca.crt`
  for pods that predate the rotation. A container restarting in place re-reads
  the same stale file and keeps failing; restart counts climb without
  progress. This does **not** self-heal on any useful timescale.

  **Order matters: restart the CNI/networking stack FIRST.** The worst
  offenders are *not* crashlooping — they are `Running` with a stale CA, so a
  symptom-based crashloop sweep skips them, yet they sit on the critical path
  for every new pod. In the drill the `ContainerCreating` backlog would not
  drain because `cozy-multus` (and behind it cilium + kube-ovn) failed its
  per-pod apiserver call
  (`Multus: ... Get https://10.96.0.1:443/... x509: unknown authority`), so
  no recreated pod could get networking. Roll the whole networking stack
  before sweeping anything else — and notReady drops sharply once it lands:

  ```sh
  kubectl -n cozy-multus  rollout restart ds/cozy-multus
  kubectl -n cozy-cilium  rollout restart ds/cilium ds/cilium-envoy deploy/cilium-operator
  kubectl -n cozy-kubeovn rollout restart ds/kube-ovn-cni ds/ovs-ovn \
    deploy/kube-ovn-controller deploy/ovn-central
  ```

  (These are hostNetwork pods, so they do not need CNI to restart — they come
  back on the new CA and unblock everyone else. The PodSecurity
  `restricted:latest` warnings they print on restart are pre-existing and
  harmless.)

  **Then recreate the remaining pods** so they get a fresh projection. Proven
  both ways in the drill — a fresh `run` pod reached `/healthz` immediately,
  and deleting a crashlooping pod brought it Ready in seconds:

  ```sh
  # confirm the mechanism once:
  kubectl run castest --image=registry.k8s.io/e2e-test-images/agnhost:2.47 \
    --restart=Never --command -- sleep 300
  kubectl exec castest -- sh -c 'curl -sS \
    --cacert /var/run/secrets/kubernetes.io/serviceaccount/ca.crt \
    -H "Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)" \
    https://10.96.0.1:443/healthz'   # -> ok

  # then recreate-until-clean (managed pods recreate themselves):
  for r in $(seq 1 8); do
    stuck=$(kubectl get pods -A --no-headers \
      | awk '$4=="CrashLoopBackOff"||$4=="Error"{print $1" "$2}')
    [ -z "$stuck" ] && echo clean && break
    echo "$stuck" | while read ns p; do kubectl -n "$ns" delete pod "$p" --wait=false; done
    sleep 45
  done
  ```

  Crashloop-sweeping only catches pods that *fail loudly*. Measure against a
  known-healthy apiserver (`kubectl --server https://<healthy-node>:6443`).

  **Then the quiet blockers: every webhook backend and aggregated apiserver.**
  This was the long tail of the drill. Admission-webhook and
  aggregated-API pods stay `Running` on a stale CA, but their
  `SubjectAccessReview`/validation calls to `10.96.0.1:443` fail
  `x509: unknown authority` — which surfaces indirectly as **Flux
  Kustomizations stuck `ReconciliationFailed`** on dry-run
  (`admission webhook "...metallb.io" denied the request: ... x509`, or
  `ClickHouse/... dry-run failed` behind `cozy-system/cozystack-api`). You
  chase these one error at a time unless you just roll them all. The blunt,
  correct instrument is to restart every controller Deployment in the
  platform namespaces — they are stateless operators, safe to bounce:

  ```sh
  for ns in $(kubectl get ns -o name | sed 's|namespace/||' | grep '^cozy-') \
            external-secrets kargo; do
    kubectl -n "$ns" rollout restart deploy 2>/dev/null
  done
  # enumerate webhook backends if you'd rather be surgical:
  kubectl get validatingwebhookconfigurations mutatingwebhookconfigurations \
    -o jsonpath='{range .items[*].webhooks[*]}{.clientConfig.service.namespace}/{.clientConfig.service.name}{"\n"}{end}' | sort -u
  ```

  Then force Flux to re-run the dry-runs (it won't retry fast enough on its
  own): `kubectl -n cozy-fluxcd annotate kustomization <k> reconcile.fluxcd.io/requestedAt="$(date -u +%FT%TZ)" --overwrite`
  for each not-`Ready` one. A stateful DB pod stuck in `Error` (its cached
  token/CA died when the node did) just needs the same delete-to-recreate;
  its operator brings it back.

The through-line for all of #1: **a Kubernetes CA rotation does not
gracefully propagate to already-running workloads.** Anything holding a
projected serviceaccount CA from before the rotation — CNI, webhooks,
aggregated APIs, controllers, DB pods — keeps using the dead CA until its
pod is recreated. Plan to restart essentially the whole non-static-pod
control plane, in dependency order: CNI first, then webhook/aggregated-API
backends, then the rest.

Needs intervention #2 — a wedged control-plane node (budget for it):

- **A control-plane node wedged with its apiserver stuck on
  `bind: address already in use` for `:6443`.** In the drill, one node's
  apiserver static pod lost the `:6443` bind race against its own
  terminating instance (hostNetwork, so the port lives in the host netns).
  The restart count then froze — because that node's **kubelet had also
  fallen out** (`kubectl get node <n>` →
  `NodeStatusUnknown: Kubelet stopped posting node status`): its client cert
  was signed by the old CA and it missed the rotation window while its local
  apiserver was down, so it was locked out and stopped reconciling static
  pods. Two failures reinforcing each other; neither cleared on its own.

  **Recovery is a reboot of the affected node** —
  `talosctl -e <ip> -n <ip> reboot`. On boot the kubelet re-bootstraps a
  client cert against the new CA and the apiserver static pod re-renders on
  a free port. Safe because the other two control planes keep etcd quorum
  (verify first: `talosctl -e <healthy-ip> -n <healthy-ip> etcd members` →
  ≥2 healthy) and the wedged node's pods have already been rescheduled.
  Diagnose these from Talos, not kubectl — that node's API is the one down:
  `talosctl -e <ip> -n <ip> logs -k kube-system/kube-apiserver-<node>:kube-apiserver`
  and `talosctl -e <ip> -n <ip> service kubelet`.

Expect the not-ready pod count to *spike* (the drill hit ~138) before it
falls: a `NotReady` node's pods enter `Terminating` and re-`ContainerCreating`
elsewhere, so the count double-counts pods mid-flight. Measure against a
known-healthy apiserver (`kubectl --server https://<healthy-node>:6443`), not
the VIP, so a failing-over apiserver doesn't skew the read. Full convergence
back to near-zero took ~15 minutes including the reboot.

If a pod is *still* crashlooping after all three nodes are `Ready`, all three
apiservers are `1/1`, and `kube-root-ca.crt` shows the new fingerprint, that
is a finding — investigate it, don't wait longer.

### 5. Reconcile the three copies of the CA

The new CA now exists on the nodes. Three off-node artifacts still trust the
old one and must be updated in the same window, or the next cold boot / OIDC
login / breakglass mint fails against a CA the cluster no longer has:

1. **Custody bundle `secrets.yaml`.** The bundle's `certs.k8s` is the genesis
   Kubernetes CA; a cold boot regenerates the cluster from it. Pull the live
   CA from a control-plane machine config and swap it in (the live MC is
   multi-doc; the CA carries both `crt` and `key` on a control plane):

   ```sh
   talosctl -e <ip> -n <ip> get mc v1alpha1 -o yaml > "$MINT/live-mc.yaml"
   # extract cluster.ca {crt,key}, write into secrets.yaml certs.k8s,
   # leave certs.os / certs.etcd / certs.k8saggregator untouched
   ```

   Confirm with a dry-run apply from the refreshed bundle — it must report
   **`No changes`** (the on-node config already matches; you are only
   re-aligning the genesis secret):

   ```sh
   cp /dev/shm/guardian-custody/talm/secrets.yaml "$MINT/secrets.yaml"
   talm apply --dry-run -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml \
     --root "$MINT" --talosconfig "$MINT/talosconfig"
   ```

2. **Committed public CA** `src/infrastructure/bootstrap/guardian-mgmt/cluster-ca.crt`.
   `aspect infra auth --platform-agent` pins OIDC's server-CA trust from this
   file on fresh machines. Overwrite it with the new CA's public cert (from a
   freshly minted kubeconfig's `certificate-authority-data`) and commit it.

3. **`~/.kube/config`** on every operator machine. The OIDC context embeds
   the server CA; it will fail
   `x509: certificate signed by unknown authority` until re-pinned:

   ```sh
   kubectl config set-cluster guardian-mgmt --embed-certs \
     --certificate-authority=<new-ca.crt>
   ```

### 6. Re-archive custody and wipe

The bundle changed (step 5.1), so snapshot it and destroy the plaintext:

```sh
aspect infra custody --action create --yes   # proves round trip, shreds sources
aspect infra custody --action wipe --yes      # in case anything was re-restored
# then shred the mint root:
find "$MINT" -type f -exec shred -u {} + && rm -rf "$MINT"
```

Refresh the offline copies of the repo (their old snapshots still restore an
old-CA bundle — harmless for DR of the *other* members, but the k8s CA in
them is stale).

### 7. Validate recovery

- All three `kube-apiserver-*` pods `1/1 Running`.
- Zero not-ready pods cluster-wide
  (`kubectl get pods -A | grep -v Running | grep -v Completed`).
- All Flux Kustomizations and HelmReleases `Ready=True`.
- A fresh admin mint works and carries the **new** issuer and the **8h**
  lifetime; the OIDC context (`kubectl auth whoami`) returns
  `platform-agent`.
- An *old* admin kubeconfig is now refused (`Unauthorized`).

## Drill log

Append one row per rotation (drill or real). This is the SOC2 evidence trail
for credential hygiene.

| Date | Type | CA | Result | Convergence | Notes |
|---|---|---|---|---|---|
| 2026-07-07 | Drill (planned) | Kubernetes API | PASS (hands-on recovery) | ~40 min to full green | First rotation. Retired 6+ year-long admin certs seen in audit logs. Invocation gotchas: VIP unreachable from VPS → `--k8s-endpoint <nodeIP>`; `-n`/`-e` required alongside `--control-plane-nodes`. Recovery was NOT automatic — the rotation does not propagate to running workloads (stale projected SA CA): had to roll CNI (multus/cilium/kube-ovn) first, recreate ~47 crashlooping pods, then roll all platform-namespace controllers + webhook/aggregated-API backends (metallb webhook & cozystack-api were the Flux dry-run blockers), and re-trigger Flux. Separately, **ash-earth wedged** (apiserver `:6443` bind race + kubelet locked out on old-CA client cert) → `talosctl reboot` recovered it; etcd quorum (2/3) held throughout. Reconciled bundle `secrets.yaml`, committed `cluster-ca.crt`, `~/.kube/config`; re-archived custody (snapshot c1836abb). |
