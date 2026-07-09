# Certificate rotation

Rotates the Kubernetes API issuing CA on a quarterly cadence (SOC 2 claim:
"the cluster CA rotates every 90 days via an automated, drilled procedure").
Leaves the Talos API CA, etcd CA, and OpenBao seal untouched. Admin
kubeconfig lifetime is a separate, rotation-free knob:
`cluster.adminKubeconfig.certLifetime` in `talm/values.yaml` (8h — expiry is
the revocation for the unrevocable `system:masters` cert).

The rotation itself only re-issues control-plane certs; a running pod reads
its projected CA bundle once at startup and trusts the dead CA forever
after. So the procedure is: **rotate** the CA, **sweep** every pod (a pod
that comes back is a pod some controller says should exist; anything bare
dies for good), **monitor** until the cluster-wide trust-failure log query
runs dry.

Node map: ash-earth `206.223.228.101`, ash-wind `45.250.254.119`,
ash-water `206.223.228.87`.

## 1. Rotate

```sh
# Restore custody, assemble a tmpfs mint root (never in the repo tree)
aspect infra custody --action restore --yes
MINT=/dev/shm/guardian-talm-mint
rm -rf "$MINT" && mkdir -m 700 "$MINT"
cp -a src/infrastructure/talm/. "$MINT/"
cp /dev/shm/guardian-custody/talm/{secrets.yaml,talm.key,talosconfig} "$MINT/"

# Rotate (runs as dry-run first; re-run with --dry-run=false to execute).
# --k8s-endpoint MUST be a public node IP — the VIP is not routable from
# the VPS; -n/-e are required alongside --control-plane-nodes. Expected
# output: add-accepted -> make-issuing -> verify OK -> remove-old -> verify OK.
CP=206.223.228.101,45.250.254.119,206.223.228.87
talosctl --talosconfig "$MINT/talosconfig" rotate-ca --talos=false \
  -e "$CP" -n "$CP" --control-plane-nodes "$CP" \
  --k8s-endpoint 206.223.228.101:6443 \
  --with-docs=false --with-examples=false \
  -o "$MINT/talosconfig.rotated"          # then: --dry-run=false

# Quorum held?
talosctl --talosconfig "$MINT/talosconfig" -e 206.223.228.101 -n 206.223.228.101 etcd members

# Re-pin the three off-node CA copies from the live machine config:
talosctl --talosconfig "$MINT/talosconfig" -e 206.223.228.101 -n 206.223.228.101 \
  get mc v1alpha1 -o yaml > "$MINT/live-mc.yaml"
yq -r '.spec.cluster.ca.crt' "$MINT/live-mc.yaml" | base64 -d > "$MINT/cluster-ca.crt"
#  a. operator kubeconfig — do this FIRST, kubectl is pinned to the dead CA
kubectl config set-cluster guardian-mgmt --embed-certs \
  --certificate-authority="$MINT/cluster-ca.crt"
#  b. committed OIDC trust pin (public cert only) — commit via the drill-log PR
cp "$MINT/cluster-ca.crt" src/infrastructure/bootstrap/guardian-mgmt/cluster-ca.crt
#  c. custody bundle genesis certs (leave certs.os/etcd/k8saggregator alone)
CRT=$(yq -r '.spec.cluster.ca.crt' "$MINT/live-mc.yaml") \
KEY=$(yq -r '.spec.cluster.ca.key' "$MINT/live-mc.yaml") \
yq -i '.certs.k8s.crt = strenv(CRT) | .certs.k8s.key = strenv(KEY)' \
  /dev/shm/guardian-custody/talm/secrets.yaml
```

## 2. Sweep pods

```sh
# CNI first — a stale CNI pod blocks ALL new pod networking, so nothing
# else can come back until these have:
for ds in cozy-multus/cozy-multus cozy-cilium/cilium cozy-cilium/cilium-envoy \
          cozy-kubeovn/kube-ovn-cni cozy-kubeovn/ovs-ovn; do
  kubectl -n "${ds%%/*}" rollout restart "ds/${ds##*/}"
  kubectl -n "${ds%%/*}" rollout status  "ds/${ds##*/}" --timeout=10m
done

# Every controller-owned workload, cluster-wide. Rolling: surge/PDB/readiness
# semantics hold per workload, so availability survives the churn (~15 min).
kubectl get deploy,sts,ds -A --no-headers \
  -o custom-columns='NS:.metadata.namespace,KIND:.kind,NAME:.metadata.name' |
while read -r ns kind name; do
  kubectl -n "$ns" rollout restart "$(tr '[:upper:]' '[:lower:]' <<<"$kind")/$name"
done

# CNPG instance pods are owned by Cluster CRs, not StatefulSets — delete one
# at a time per cluster; the operator handles switchover and rebuild:
kubectl get clusters.postgresql.cnpg.io -A --no-headers | while read -r ns name _; do
  want=$(kubectl -n "$ns" get clusters.postgresql.cnpg.io "$name" -o jsonpath='{.spec.instances}')
  for pod in $(kubectl -n "$ns" get pods -l "cnpg.io/cluster=$name" -o jsonpath='{.items[*].metadata.name}'); do
    kubectl -n "$ns" delete pod "$pod"
    until [ "$(kubectl -n "$ns" get clusters.postgresql.cnpg.io "$name" \
      -o jsonpath='{.status.readyInstances}')" = "$want" ]; do sleep 5; done
  done
done

# Bare pods (no ownerReferences): delete; nothing recreates them — that is
# the point. Job pods are skipped, they age out on their own.
kubectl get pods -A -o json | jq -r '.items[]
  | select((.metadata.ownerReferences // []) | length == 0)
  | "\(.metadata.namespace) \(.metadata.name)"' |
while read -r ns pod; do kubectl -n "$ns" delete pod "$pod" --wait=false; done
```

## 3. Monitor for recovery

```sh
# Converged?
kubectl get pods -A --no-headers | grep -vE 'Running|Completed'   # -> empty
kubectl get kustomization -A --no-headers | awk '$4!="True"'      # -> empty

# Sweep-until-dry: any pod still trusting the dead CA surfaces here.
# Delete every hit, wait for the 15m window to advance, re-run until zero
# rows. Some streams lack pod metadata (velero node-agent, CNPG logging_pod)
# — read _msg to attribute those by hand. The standing X509TrustFailure
# alert watches this same query continuously; here it is the acceptance gate.
kubectl port-forward -n tenant-root svc/vlselect-generic 9471:9471 &   # re-establish after the sweep recycles vlselect
curl -s 127.0.0.1:9471/select/logsql/query --data-urlencode \
  'query=_time:15m log_source:container_log "certificate signed by unknown authority"
   | stats by (kubernetes_namespace_name, kubernetes_pod_name) count()' |
jq -r '[.kubernetes_namespace_name, .kubernetes_pod_name] | @tsv' |
while read -r ns pod; do [ -n "$pod" ] && kubectl -n "$ns" delete pod "$pod"; done

# Acceptance — all of these, or the rotation is not done:
kubectl auth whoami                       # OIDC login works against the new pin
kubectl --kubeconfig <old-admin-kubeconfig> get nodes   # MUST be refused (Unauthorized)
# x509 query above: zero rows

# Close out: mint bundle dry-run applies clean, then re-archive custody and
# destroy all plaintext.
talm apply --dry-run -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml \
  --root "$MINT" --talosconfig "$MINT/talosconfig"      # -> no changes
aspect infra custody --action create --yes
aspect infra custody --action wipe --yes
find "$MINT" -type f -exec shred -u {} + && rm -rf "$MINT"
```

Contingencies, each hit live on 2026-07-07:
- Node `NotReady`, kubelet logging old-CA x509 (`NodeStatusUnknown`):
  `talosctl reboot` the node — the kubelet re-bootstraps its client cert.
- OpenBao returns plain `403 permission denied` (no x509 hint) and
  ClusterSecretStores go `InvalidProviderConfig`: the sweep already recycled
  OpenBao; force ESO to retry now instead of on interval:
  `kubectl annotate externalsecret -A --all force-sync=$(date +%s) --overwrite`
- Flux Kustomizations stuck `ReconciliationFailed` on webhook/aggregated-API
  dry-run x509: the backends are recycled by the sweep; re-trigger with
  `kubectl annotate kustomization -A --all reconcile.fluxcd.io/requestedAt=$(date +%s) --overwrite`

## Drill log

Append one row per rotation (drill or real). This is the SOC 2 evidence
trail for credential hygiene.

| Date | Type | CA | Result | Convergence | Notes |
|---|---|---|---|---|---|
| 2026-07-07 | Drill (planned) | Kubernetes API | PASS (hands-on recovery) | ~40 min to full green | First rotation. Retired 6+ year-long admin certs seen in audit logs. Invocation gotchas: VIP unreachable from VPS → `--k8s-endpoint <nodeIP>`; `-n`/`-e` required alongside `--control-plane-nodes`. Recovery was NOT automatic — the rotation does not propagate to running workloads (stale projected SA CA): had to roll CNI (multus/cilium/kube-ovn) first, recreate ~47 crashlooping pods, then roll all platform-namespace controllers + webhook/aggregated-API backends (metallb webhook & cozystack-api were the Flux dry-run blockers), and re-trigger Flux. Separately, **ash-earth wedged** (apiserver `:6443` bind race + kubelet locked out on old-CA client cert) → `talosctl reboot` recovered it; etcd quorum (2/3) held throughout. Reconciled bundle `secrets.yaml`, committed `cluster-ca.crt`, `~/.kube/config`; re-archived custody (snapshot c1836abb). |
| 2026-07-07 | Fallout (next day) | Kubernetes API | Swept clean | ~1 h intermittent | The triage-style recovery missed ~14 Running-and-Ready pods with silently dead API watches (stale projected CA): a CoreDNS replica served a frozen DNS snapshot (NXDOMAIN for post-rotation Services), a root-ingress replica retried dead upstreams on live prod traffic (TTFB degradation felt from Seattle), plus metallb/velero/kubevirt/CNPG/alertmanager/vmalert stragglers. Swept via the VictoriaLogs x509 query until dry. Motivated the proactive full-sweep shape of this runbook and the standing X509TrustFailure alert. |
