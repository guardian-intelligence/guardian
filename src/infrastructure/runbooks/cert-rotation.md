# Certificate rotation

Rotates the Kubernetes API issuing CA on a quarterly cadence (SOC 2 claim:
"the cluster CA rotates every 90 days via an automated, drilled procedure").
Leaves the Talos API CA, etcd CA, and OpenBao seal untouched. Admin
kubeconfig lifetime is a separate, rotation-free knob:
`cluster.adminKubeconfig.certLifetime` in `talm/values.yaml` (8h — expiry is
the revocation for the unrevocable `system:masters` cert).

A rotation is three moves: **rotate** the CA, **recycle** every node (a
process only reads the projected CA bundle at startup, so running workloads
keep trusting the dead CA until their pods are recreated — reboot-by-drain
recreates everything, kubelet client certs included), **reconcile** the
three off-node CA copies. Do not skip the recycle and triage pods instead;
that was the 2026-07-07 drill's 40-minute mistake.

Node map: ash-earth `206.223.228.101`, ash-wind `45.250.254.119`,
ash-water `206.223.228.87`.

## Execute

```sh
# 1. Restore custody, assemble a tmpfs mint root (never in the repo tree)
aspect infra custody --action restore --yes
MINT=/dev/shm/guardian-talm-mint
rm -rf "$MINT" && mkdir -m 700 "$MINT"
cp -a src/infrastructure/talm/. "$MINT/"
cp /dev/shm/guardian-custody/talm/{secrets.yaml,talm.key,talosconfig} "$MINT/"

# 2. Rotate (runs as dry-run first; re-run with --dry-run=false to execute).
#    --k8s-endpoint MUST be a public node IP — the VIP is not routable from
#    the VPS; -n/-e are required alongside --control-plane-nodes.
CP=206.223.228.101,45.250.254.119,206.223.228.87
talosctl --talosconfig "$MINT/talosconfig" rotate-ca --talos=false \
  -e "$CP" -n "$CP" --control-plane-nodes "$CP" \
  --k8s-endpoint 206.223.228.101:6443 \
  --with-docs=false --with-examples=false \
  -o "$MINT/talosconfig.rotated"          # then: --dry-run=false

# 3. Recycle every node, one at a time (drain respects PDBs; reboot
#    re-bootstraps the kubelet cert and recreates every pod on the node)
for node in ash-earth:206.223.228.101 ash-wind:45.250.254.119 ash-water:206.223.228.87; do
  n="${node%%:*}" ip="${node##*:}"
  kubectl drain "$n" --ignore-daemonsets --delete-emptydir-data --timeout=10m
  talosctl --talosconfig "$MINT/talosconfig" -e "$ip" -n "$ip" reboot --wait
  kubectl uncordon "$n"
  kubectl wait node "$n" --for=condition=Ready --timeout=10m
done

# 4. Reconcile the three off-node CA copies:
#    a. custody bundle: copy cluster.ca {crt,key} from the live MC into
#       secrets.yaml certs.k8s (leave certs.os/etcd/k8saggregator alone)
talosctl --talosconfig "$MINT/talosconfig" -e 206.223.228.101 -n 206.223.228.101 \
  get mc v1alpha1 -o yaml > "$MINT/live-mc.yaml"
#    b. committed OIDC trust pin (public cert only), then commit:
#       src/infrastructure/bootstrap/guardian-mgmt/cluster-ca.crt
#    c. operator kubeconfigs:
kubectl config set-cluster guardian-mgmt --embed-certs \
  --certificate-authority=<new-ca.crt>

# 5. Re-archive custody, destroy plaintext, append to the drill log below
aspect infra custody --action create --yes
aspect infra custody --action wipe --yes
find "$MINT" -type f -exec shred -u {} + && rm -rf "$MINT"
```

## Verify

```sh
# after step 2: rotation walked all four phases and verified twice
#   add-accepted -> make-issuing -> verify OK -> remove-old -> verify OK

# after each node in step 3: quorum held, node rejoined
talosctl --talosconfig "$MINT/talosconfig" -e "$ip" -n "$ip" etcd members
kubectl get nodes                                  # all Ready

# after step 4a: dry-run apply from the refreshed bundle reports NO changes
talm apply --dry-run -f nodes/<node>.yaml -f nodes/<node>-overlay.yaml \
  --root "$MINT" --talosconfig "$MINT/talosconfig"

# acceptance — all four, or the rotation is not done:
kubectl get pods -A | grep -v Running | grep -v Completed   # empty
kubectl get kustomization -A | grep -v True                 # empty
kubectl auth whoami                                         # platform-agent (OIDC re-pinned)
# zero trust failures cluster-wide (the standing X509TrustFailure alert
# watches this same query continuously; here it is the acceptance gate):
kubectl port-forward -n tenant-root svc/vlselect-generic 9471:9471 &
curl -s 127.0.0.1:9471/select/logsql/query --data-urlencode \
  'query=_time:15m log_source:container_log "certificate signed by unknown authority"
   | stats by (kubernetes_namespace_name, kubernetes_pod_name) count()'   # no rows
```

An old admin kubeconfig must now be refused (`Unauthorized`); a fresh mint
carries the new issuer and the 8h lifetime. If any pod still logs trust
failures after the recycle, that pod is a finding — investigate, don't wait.

## Drill log

Append one row per rotation (drill or real). This is the SOC 2 evidence
trail for credential hygiene.

| Date | Type | CA | Result | Convergence | Notes |
|---|---|---|---|---|---|
| 2026-07-07 | Drill (planned) | Kubernetes API | PASS (hands-on recovery) | ~40 min to full green | First rotation. Retired 6+ year-long admin certs seen in audit logs. Invocation gotchas: VIP unreachable from VPS → `--k8s-endpoint <nodeIP>`; `-n`/`-e` required alongside `--control-plane-nodes`. Recovery was NOT automatic — the rotation does not propagate to running workloads (stale projected SA CA): had to roll CNI (multus/cilium/kube-ovn) first, recreate ~47 crashlooping pods, then roll all platform-namespace controllers + webhook/aggregated-API backends (metallb webhook & cozystack-api were the Flux dry-run blockers), and re-trigger Flux. Separately, **ash-earth wedged** (apiserver `:6443` bind race + kubelet locked out on old-CA client cert) → `talosctl reboot` recovered it; etcd quorum (2/3) held throughout. Reconciled bundle `secrets.yaml`, committed `cluster-ca.crt`, `~/.kube/config`; re-archived custody (snapshot c1836abb). |
| 2026-07-07 | Fallout (next day) | Kubernetes API | Swept clean | ~1 h intermittent | The triage-style recovery missed ~14 Running-and-Ready pods with silently dead API watches (stale projected CA): a CoreDNS replica served a frozen DNS snapshot (NXDOMAIN for post-rotation Services), a root-ingress replica retried dead upstreams on live prod traffic (TTFB degradation felt from Seattle), plus metallb/velero/kubevirt/CNPG/alertmanager/vmalert stragglers. Swept via the VictoriaLogs x509 query until dry. Motivated this runbook's recycle-by-default rewrite and the standing X509TrustFailure alert. |
