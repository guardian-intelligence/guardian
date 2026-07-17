# Wiped-node drill

The remaining M1 drill: prove one node can be lost entirely — ungraceful,
with etcd-member and Node-object debris left behind — and return to the
cluster from Git + custody alone, without breaching the 60-second
public-edge budget.

Disk encryption does NOT ride this drill: nodeID-keyed LUKS2 was
attempted on ash-earth 2026-07-09 and Talos refused it — the Latitude
boards ship a degenerate SMBIOS machine UUID
(`00000000-0000-0000-0000-905a…`) and the nodeID key handler fails its
entropy check, leaving STATE unprovisionable. The boards DO carry TPM
2.0 (`tpm_tis MSFT0101`, and Talos logs "TPM is ready for disk
encryption operations"), so encryption at rest returns as TPM keying
with the M4 SecureBoot work.

One node at a time, by explicit IP, healthiest-cluster-first. Wait for
full recovery (node Ready, DRBD resynced, public edge steady) before the
next node. A node whose loss breaches 60s of public-edge disruption is
load-bearing — stop and fix before continuing.

Node map: ash-earth `206.223.228.101`, ash-wind `45.250.254.119`,
ash-water `206.223.228.87`. VLAN: .11/.12/.13.

## Preconditions

- Cluster green (`tools/ops/cluster-watch --status`), etcd 3/3, no
  degraded DRBD resources (filter to the DRBD layer — STORAGE-only
  resources report `Created`, never `UpToDate`, and are not replicas):
  `kubectl get nodes; kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor resource list | grep DRBD | grep -v UpToDate | head`
- Custody restored (`aspect infra custody --action restore --yes`), mint
  root assembled per cert-rotation.md step 1.
- Fresh etcd snapshot exists in R2 (belt and suspenders):
  `kubectl -n tenant-root create job --from=cronjob/talos-backup pre-drill-$(date +%s)` — wait for Complete.
- Public-edge watch running in a second terminal, timestamps on:
  `while :; do printf '%s %s\n' "$(date -u +%FT%T.%3NZ)" "$(curl -so /dev/null -w '%{http_code}' -m 2 https://guardianintelligence.org/)"; sleep 1; done | grep --line-buffered -v ' 200'`
  (prints only non-200 seconds; the disruption window is the span of that output).
- For a PLANNED node outage, drain the edge first: measured 2026-07-09,
  Cloudflare kept sending ~25-30% of requests to a dead origin for
  ~4m16s before evicting it — the declared 60s monitor plus edge
  propagation is nowhere near the 60s budget, and a wiped node
  blackholes SYNs (no RST) so every routed request stalls the full
  origin-connect timeout. Disable the node's origin in
  `bootstrap/guardian-mgmt-dns` (origin `enabled = false`), apply, and
  verify zero non-200s for 60s before wiping; revert after the node
  rejoins. The unplanned-loss exposure this reveals is a standing
  finding, not something this runbook can fix.

## Execute (per node)

```sh
MINT=/dev/shm/guardian-talm-mint
IP=<node public IP>   # explicit, one node only
NODE=<k8s node name>

# 1. Ungraceful wipe — this IS the drill: no drain, no etcd leave.
#    --wipe-mode all zeroes the whole system disk INCLUDING the boot
#    partition: despite --reboot, the machine comes back with nothing
#    to boot and goes dark (no ping, no apid) — drilled 2026-07-09 on
#    ash-earth. Recovery is a Latitude reinstall (step 1b), which is
#    also what a real dead-node replacement looks like, so the drill
#    exercises the true path.
talosctl --talosconfig "$MINT/talosconfig" -e "$IP" -n "$IP" \
  reset --graceful=false --reboot --wipe-mode all

# 1b. Latitude reinstall to get a bootable substrate back (server IDs:
#     ash-earth sv_vAPXaMxKM5epz, ash-water sv_8mop5gZo8Njxv, ash-wind
#     sv_nPRbajqEB5koM). The outcome is bimodal (see
#     latitude-reimage-behavior notes): either a real Ubuntu install
#     (ssh:22 opens) or the machine lands netbooted in Talos
#     maintenance mode (insecure apid :50000). Poll BOTH and branch:
#     maintenance mode → straight to step 3 with
#     --skip-resource-validation; Ubuntu → kexec boot-to-talos per
#     cold-boot-bootstrap.md, then step 3.
TOK=$(cat /dev/shm/guardian-custody/latitude.token)
curl -s -X POST -H "Authorization: $TOK" -H "Content-Type: application/json" \
  "https://api.latitude.sh/servers/<server-id>/reinstall" \
  -d '{"data":{"type":"reinstalls","attributes":{"operating_system":"ubuntu_24_04_x64_lts","hostname":"<node>","ssh_keys":["ssh_W9EKa3oBbaRoB"]}}}'

# 2. Debris cleanup from a surviving node (the dead member blocks the
#    rejoin if left):
talosctl --talosconfig "$MINT/talosconfig" -e <other-IP> -n <other-IP> etcd members
talosctl --talosconfig "$MINT/talosconfig" -e <other-IP> -n <other-IP> \
  etcd remove-member <hex-id-of-wiped-node>
kubectl delete node "$NODE"

# 3. Reapply config from Git + custody (node is in maintenance mode,
#    Talos API insecure on :50000). The overlay side-patch is NOT
#    optional: the node file alone renders the discovery-fallback
#    hostname (talos-<id>) and the wrong link MTU — the overlay carries
#    the hand-maintained hostname/VLAN/MTU truth.
talm apply --talosconfig "$MINT/talosconfig" \
  -f "$MINT/nodes/<node>.yaml" -f "$MINT/nodes/<node>-overlay.yaml" \
  --insecure

# 4. Watch install + join (volume provisioning is where a config
#    regression bites — a failed STATE shows `phase: failed` with the
#    reason in errorMessage):
talosctl --talosconfig "$MINT/talosconfig" -e "$IP" -n "$IP" get volumestatus
kubectl wait node "$NODE" --for=condition=Ready --timeout=15m

# 5. Recreate the data volume group — the Latitude reinstall wipes BOTH
#    NVMe disks, and Piraeus only runs its first-time device prep when
#    the pool is absent from the LINSTOR db; a wiped node's pool row
#    survives (state Ok, 0 KiB) so the operator never re-preps. The
#    node's DRBD resources sit in `Unknown` with lvcreate errors in the
#    satellite log until the VG exists again. Device by serial from
#    base/storage/linstor-data-pools.yaml — NEVER by /dev/nvmeXnY name:
kubectl -n cozy-linstor exec linstor-satellite.<node>-<hash> -c linstor-satellite -- \
  sh -c 'pvcreate /dev/disk/by-id/<data-disk-serial-id> && vgcreate data /dev/disk/by-id/<data-disk-serial-id>'
#    LINSTOR's DeviceManager retry loop picks the VG up within a minute
#    and starts recreating LVs + resync unprompted.

# 6. Re-place the OpenBao static seal key — every node is key-bearing
#    and the key lived on the wiped disk, so the node's OpenBao replica
#    crashloops in verify-static-seal-key (Init:Error) until the key is
#    back. Follow "Seal-key placement" in cold-boot-bootstrap.md
#    (debug-pod + streamed exec from the custody bundle; only the
#    fingerprint is ever printed), then delete the replica pod so it
#    remounts and rejoins raft.

# 7. Wait for replicated storage to heal before calling it done:
kubectl -n cozy-linstor exec deploy/linstor-controller -- linstor resource list | grep DRBD | grep -v UpToDate
```

Record in the drill log below: wall-clock outage window of the node,
public-edge disruption seconds (from the edge watch), anything manual
that was not in this runbook (that is a finding — fix the runbook or the
system).

## Latitude gotchas

- A reimage via Latitude instead of `reset --reboot` may land in Talos
  maintenance mode rather than Ubuntu, and the API can lie about
  deploy_config — check actual state, not the panel (see
  latitude-reimage-behavior memory/runbook notes).
- `install.diskSelector` pins by serial; the discovery comments in each
  node file carry both NVMe serials. A replaced drive means updating the
  serial BEFORE step 3.

## Drill log

| Date | Node | Outage window | Public-edge disruption | Findings |
|------|------|---------------|------------------------|----------|
| 2026-07-09 | ash-earth | 01:16Z → ~04:10Z (extended by two findings below) | 4m16s of ~25-30% requests stalling >10s (01:16:28–01:20:44) | (1) `--wipe-mode all` zeroes the boot partition — machine goes dark, recovery is Latitude reinstall (~10min to maintenance mode); (2) CF LB took ~4.5min to evict the dead origin vs 60s declared — pre-drain origins for planned work; (3) nodeID LUKS2 refused: degenerate SMBIOS UUID fails entropy check (STATE `phase: failed`) — recovered by reapplying unencrypted config with `-m reboot`; TPM 2.0 present for the future path. etcd remove-member + Node delete + rejoin from Git+custody worked exactly as written. |
