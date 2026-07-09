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
dies for good), **monitor** until every pod post-dates the rotation and the
cluster-wide trust-failure log query runs dry.

Node map: ash-earth `206.223.228.101`/`10.8.0.11`,
ash-wind `45.250.254.119`/`10.8.0.12`, ash-water `206.223.228.87`/`10.8.0.13`.

## 1. Rotate

```sh
# Restore custody, assemble a tmpfs mint root (never in the repo tree)
aspect infra custody --action restore --yes
MINT=/dev/shm/guardian-talm-mint
rm -rf "$MINT" && mkdir -m 700 "$MINT"
cp -a src/infrastructure/talm/. "$MINT/"
cp /dev/shm/guardian-custody/talm/{secrets.yaml,talm.key,talosconfig} "$MINT/"

# Rotate (runs as dry-run first; re-run with --dry-run=false to execute).
# Node list MUST be the VLAN IPs through ONE public endpoint: the host
# ingress firewall admits :50000 only from the VLAN, pod/join subnets, and
# the operator VPS, so apid proxy hops between nodes' PUBLIC IPs are
# silently dropped (i/o timeout at "Building current Kubernetes client").
# --k8s-endpoint is a public node IP — the VIP is not routable from the VPS.
# Expected: add-accepted -> make-issuing -> verify OK -> remove-old -> verify OK.
NODES=10.8.0.11,10.8.0.12,10.8.0.13
talosctl --talosconfig "$MINT/talosconfig" rotate-ca --talos=false \
  -e 206.223.228.101 -n "$NODES" --control-plane-nodes "$NODES" \
  --k8s-endpoint 206.223.228.101:6443 \
  --with-docs=false --with-examples=false \
  -o "$MINT/talosconfig.rotated"          # then: --dry-run=false

# Quorum held?
talosctl --talosconfig "$MINT/talosconfig" -e 206.223.228.101 -n 10.8.0.11 etcd members

# Re-pin the three off-node CA copies from the live machine config.
# (No yq on the VPS; the mc spec is a multi-doc YAML string inside JSON.)
talosctl --talosconfig "$MINT/talosconfig" -e 206.223.228.101 -n 10.8.0.11 \
  get mc v1alpha1 -o json > "$MINT/live-mc.json"
python3 - <<'EOF'
import json, yaml, base64, pathlib
m = pathlib.Path("/dev/shm/guardian-talm-mint")
spec = json.loads((m/"live-mc.json").read_text())["spec"]
cfg = next(d for d in yaml.safe_load_all(spec) if isinstance(d, dict) and "cluster" in d)
ca = cfg["cluster"]["ca"]
(m/"cluster-ca.crt").write_bytes(base64.b64decode(ca["crt"]))
p = pathlib.Path("/dev/shm/guardian-custody/talm/secrets.yaml")
s = yaml.safe_load(p.read_text())
s["certs"]["k8s"]["crt"], s["certs"]["k8s"]["key"] = ca["crt"], ca["key"]
p.write_text(yaml.safe_dump(s, default_flow_style=False, sort_keys=False))
EOF
#  a. operator kubeconfig — do this FIRST, kubectl is pinned to the dead CA
kubectl config set-cluster guardian-mgmt --embed-certs \
  --certificate-authority="$MINT/cluster-ca.crt"
#  b. committed OIDC trust pin (public cert only) — commit via the drill-log PR
cp "$MINT/cluster-ca.crt" src/infrastructure/bootstrap/guardian-mgmt/cluster-ca.crt
#  c. refresh the mint's copy too — the close-out dry-run renders from the
#     MINT root, and a stale mint secrets.yaml shows a bogus CA "downgrade"
cp /dev/shm/guardian-custody/talm/secrets.yaml "$MINT/secrets.yaml"
```

## 2. Sweep pods

The sweep window takes the platform Keycloak down while cozy-keycloak
recycles, which takes kubectl OIDC logins down with it. If a fresh token is
needed mid-sweep, mint a breakglass kubeconfig (8h lifetime, new CA):
`talosctl --talosconfig "$MINT/talosconfig" -e 206.223.228.101 -n 10.8.0.11 kubeconfig "$MINT/kubeconfig-breakglass"`.

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
# Flagger canary TARGETS are swapped for their -primary: restarting a target
# template mid-churn triggers a full canary analysis; target pods are
# recycled by the freshness pass below (pod delete, no template change).
FLAGGER_TARGETS=$(kubectl get canaries -A -o json | jq -r '.items[] | "\(.metadata.namespace)/\(.spec.targetRef.name)"')
kubectl get deploy,sts,ds -A --no-headers \
  -o custom-columns='NS:.metadata.namespace,KIND:.kind,NAME:.metadata.name' |
while read -r ns kind name; do
  grep -qx "$ns/$name" <<<"$FLAGGER_TARGETS" && name="$name-primary"
  kubectl -n "$ns" rollout restart "$(tr '[:upper:]' '[:lower:]' <<<"$kind")/$name"
done

# CNPG instance pods are owned by Cluster CRs, not StatefulSets. REPLICAS
# FIRST, PRIMARY LAST: deleting the primary while a standby still runs the
# old CA wedges the failover ("Failing over" forever — the stale standby's
# instance manager cannot coordinate promotion). keycloak-db is the platform
# Keycloak's DB, so that wedge takes OIDC down cluster-wide.
kubectl get clusters.postgresql.cnpg.io -A --no-headers | while read -r ns name _; do
  want=$(kubectl -n "$ns" get clusters.postgresql.cnpg.io "$name" -o jsonpath='{.spec.instances}')
  pods=$(kubectl -n "$ns" get pods -l "cnpg.io/cluster=$name" -o json | jq -r \
    '.items | sort_by(.metadata.labels["cnpg.io/instanceRole"] == "primary") | .[].metadata.name')
  for pod in $pods; do
    kubectl -n "$ns" delete pod "$pod"   # old pod may take ~2 min to terminate
                                         # (stale instance manager); wait, don't force
    until [ "$(kubectl -n "$ns" get clusters.postgresql.cnpg.io "$name" \
        -o jsonpath='{.status.readyInstances}')" = "$want" ] && \
      [ "$(kubectl -n "$ns" get clusters.postgresql.cnpg.io "$name" \
        -o jsonpath='{.status.phase}')" = "Cluster in healthy state" ]; do sleep 5; done
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

The completion invariant is **freshness**: every pod (except Job pods and
static mirror pods) must post-date the rotation. `rollout restart` alone
does not get there — operator-reconciled workloads (kubevirt, the grafana
operator) strip the restart annotation before it takes effect, OnDelete
StatefulSets (OpenBao) ignore it entirely, and Flagger target pods were
deliberately skipped above. The freshness query finds them all; delete the
stragglers directly (RAFT MEMBERS ONE AT A TIME — wait Ready between
OpenBao pods).

```sh
# Converged?
kubectl get pods -A --no-headers | grep -vE 'Running|Completed'   # -> empty (Job debris ages out)
kubectl get kustomization -A --no-headers | awk '$4!="True"'      # -> empty

# Freshness: pods older than the rotation, i.e. missed by the sweep.
# Loop: delete hits (CNPG replicas-first as above; raft one at a time),
# re-run until EMPTY. CUTOFF = rotation completion time (UTC).
CUTOFF=<rotation-end-RFC3339>
kubectl get pods -A -o json | jq -r --arg c "$CUTOFF" '.items[]
  | (.metadata.ownerReferences[0].kind // "NONE") as $k
  | select($k != "Job" and $k != "Node")
  | select(.metadata.creationTimestamp < $c)
  | "\($k)\t\(.metadata.namespace)/\(.metadata.name)\t\(.metadata.creationTimestamp)"'

# After the OpenBao pods recycle, ClusterSecretStores sit in
# InvalidProviderConfig until ESO retries — force it now:
kubectl annotate clustersecretstore --all force-sync=$(date +%s) --overwrite
kubectl annotate externalsecret -A --all force-sync=$(date +%s) --overwrite

# Trust-failure sweep: any surviving pod on the dead CA surfaces here.
# Hits from pods CREATED AFTER the cutoff are real findings; hits from
# already-recycled pods are history aging out of the window. Some streams
# lack pod metadata (velero node-agent, CNPG logging_pod) — read _msg to
# attribute those. The standing X509TrustFailure alert watches this same
# query continuously; here it is the acceptance gate.
kubectl port-forward -n tenant-root svc/vlselect-generic 9471:9471 &   # re-establish after the sweep recycles vlselect
curl -s 127.0.0.1:9471/select/logsql/query --data-urlencode \
  'query=_time:15m log_source:container_log "certificate signed by unknown authority"
   | stats by (kubernetes_namespace_name, kubernetes_pod_name) count()'   # -> zero rows

# Acceptance — all of these, or the rotation is not done:
kubectl auth whoami                       # OIDC login works against the new pin
# freshness query: empty; x509 query: zero rows; old admin kubeconfigs are
# covered by construction (8h lifetime + the old CA left the accepted set)

# Close out: mint bundle dry-run shows NO cert diff, then re-archive custody
# and destroy all plaintext.
talm apply --dry-run -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml \
  --root "$MINT" --talosconfig "$MINT/talosconfig"
aspect infra custody --action create --yes
aspect infra custody --action wipe --yes
find "$MINT" -type f -exec shred -u {} + && rm -rf "$MINT"
```

Contingencies, each hit live (2026-07-07 and 2026-07-09):
- Node `NotReady`, kubelet logging old-CA x509 (`NodeStatusUnknown`):
  `talosctl reboot` the node — the kubelet re-bootstraps its client cert.
- CNPG cluster stuck `Failing over` with a lone surviving standby: the
  standby's stale instance manager can't coordinate promotion — delete that
  standby pod; the fresh pod completes the failover within a minute.
- CNPG replica crashlooping with `could not locate a valid checkpoint
  record` (unclean kill mid-write): delete the instance's pod AND PVC; the
  operator re-clones it from the primary (comes back under a new instance
  number — normal).
- OpenBao returns plain `403 permission denied` (no x509 hint) and
  ClusterSecretStores go `InvalidProviderConfig`: recycle OpenBao pods one
  at a time, then the ESO force-sync above.
- Flux Kustomizations stuck `ReconciliationFailed` on webhook/aggregated-API
  dry-run x509: recycle those backends, re-trigger with
  `kubectl annotate kustomization -A --all reconcile.fluxcd.io/requestedAt=$(date +%s) --overwrite`

## Drill log

Append one row per rotation (drill or real). This is the SOC 2 evidence
trail for credential hygiene.

| Date | Type | CA | Result | Convergence | Notes |
|---|---|---|---|---|---|
| 2026-07-07 | Drill (planned) | Kubernetes API | PASS (hands-on recovery) | ~40 min to full green | First rotation. Retired 6+ year-long admin certs seen in audit logs. Invocation gotchas: VIP unreachable from VPS → `--k8s-endpoint <nodeIP>`; `-n`/`-e` required alongside `--control-plane-nodes`. Recovery was NOT automatic — the rotation does not propagate to running workloads (stale projected SA CA): had to roll CNI (multus/cilium/kube-ovn) first, recreate ~47 crashlooping pods, then roll all platform-namespace controllers + webhook/aggregated-API backends (metallb webhook & cozystack-api were the Flux dry-run blockers), and re-trigger Flux. Separately, **ash-earth wedged** (apiserver `:6443` bind race + kubelet locked out on old-CA client cert) → `talosctl reboot` recovered it; etcd quorum (2/3) held throughout. Reconciled bundle `secrets.yaml`, committed `cluster-ca.crt`, `~/.kube/config`; re-archived custody (snapshot c1836abb). |
| 2026-07-07 | Fallout (next day) | Kubernetes API | Swept clean | ~1 h intermittent | The triage-style recovery missed ~14 Running-and-Ready pods with silently dead API watches (stale projected CA): a CoreDNS replica served a frozen DNS snapshot (NXDOMAIN for post-rotation Services), a root-ingress replica retried dead upstreams on live prod traffic (TTFB degradation felt from Seattle), plus metallb/velero/kubevirt/CNPG/alertmanager/vmalert stragglers. Swept via the VictoriaLogs x509 query until dry. Motivated the proactive full-sweep shape of this runbook and the standing X509TrustFailure alert. |
| 2026-07-09 | Drill (planned) | Kubernetes API | PASS | ~7 min to bulk green; ~2 h to full acceptance | Second rotation; first run of the rotate→sweep→monitor shape. Zero public-edge downtime (gi.org 200 throughout). Found live: (1) the #555 host firewall drops apid proxy hops between node PUBLIC IPs → rotate-ca must target VLAN node IPs through one public endpoint; (2) no `yq` on the VPS → python extraction, and the mc spec is multi-doc YAML inside JSON; (3) sweeping the CNPG **primary before replicas** wedged keycloak-db `Failing over` (stale standby couldn't coordinate promotion) → platform Keycloak down ~25 min → kubectl OIDC dark cluster-wide; breakglass = `talosctl kubeconfig` from the mint; replicas-first ordering adopted; (4) `rollout restart` is silently reverted by operator-reconciled workloads (kubevirt, grafana) and ignored by OnDelete STS (OpenBao) → the **freshness query** (creationTimestamp < cutoff) is the completion invariant; it caught ~40 stragglers incl. all Flagger target pods; (5) prod postgres-products replica WAL-corrupt from an unclean kill during the OIDC-dark window (`could not locate a valid checkpoint record`) → pod+PVC delete, operator re-cloned in ~90 s; (6) stale mint secrets.yaml made the close-out dry-run show a bogus CA downgrade → refresh mint from custody after the edit. Standing finding: talm dry-run wants to ADD a `VLANConfig` multidoc (template drift vs live MC, pre-existing) — needs its own reviewed change. x509 gate dry; ESO 10/10 stores after force-sync; all 6 Flagger canaries untouched (Succeeded). |
