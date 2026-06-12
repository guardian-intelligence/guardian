# Cilium version bumps (the per-site wipe drill)

Cilium ships as machine-config substrate (`src/infrastructure-components/
cilium/`, applied by Talos as an inline bootstrap manifest). Talos applies
inline manifests **at bootstrap only** — a hot re-converge never updates
them — so every Cilium **version bump** is a deliberate wipe drill per
site, dev first. (Same-version config deltas can instead live-apply the
re-render alongside the machine-config update — see
`docs/architecture/gateway.md`.) All sites run Cilium today; this runbook
is the standing procedure.

Conventions from `docs/runbooks/aisucks-release.md` apply (KUBECONFIG,
RUNFILES_DIR, site IPs, the verself no-touch caution). SLA: the box must
be back to fully serving within **4 minutes** of `guardian up` against a
maintenance-mode node.

## 0. Preconditions (all must hold)

- Dev soaked green on the new Cilium: node Ready, all pods Running, gate
  battery green, reboot drills PASS within SLA.
- The site's `site.yaml` lists the three patches (commit them together):
  `cni-none.yaml`, `src/infrastructure-components/cilium/talos/cilium-inline.yaml`,
  `ingress-firewall.yaml`.
- Let's Encrypt budget: the wipe destroys the site's cert cache → one
  duplicate-cert issuance for that domain (limit 5/week/domain). Check the
  week's count for the domain before proceeding.
- BMC/OOB path confirmed reachable via the Latitude API (AGENTS.md) — the
  lifeline if the firewall ever locks the management plane out.
- `bazelisk test //...` green on the exact commit being converged.

## 1. Back up the database FIRST (prod especially — the corpus)

The wipe destroys PGDATA (hostPath on EPHEMERAL). No automated backups
exist yet (M5), so this dump is the only copy:

```sh
TS=$(date -u +%Y%m%dT%H%M%SZ)
kubectl -n aisucks exec postgres-0 -- pg_dump --clean --if-exists -U aisucks aisucks > /tmp/aisucks-$SITE-$TS.sql
sha256sum /tmp/aisucks-$SITE-$TS.sql | tee /tmp/aisucks-$SITE-$TS.sql.sha256
grep -c 'INSERT\|COPY' /tmp/aisucks-$SITE-$TS.sql   # expect: > 0
kubectl -n aisucks exec postgres-0 -- psql -U aisucks -t -c \
  'select count(*) from reports;'                    # record the number
```

STOP if the dump is empty but the count is not.

## 2. Convert (the wipe drill)

```sh
guardian down --yes src/sites/$SITE/site.yaml   # → maintenance mode
guardian up src/sites/$SITE/site.yaml           # Talos installs, Cilium inline
```

The node goes Ready only after Cilium runs — that wait is the CNI gate.
Immediately after the apiserver answers, recreate the db secret
(runbook §0 of aisucks-release.md); the password is new, which is fine —
the fresh postgres initializes with it.

## 3. Restore (sites with data)

```sh
kubectl -n aisucks cp /tmp/aisucks-$SITE-$TS.sql postgres-0:/tmp/restore.sql
kubectl -n aisucks exec postgres-0 -- psql -U aisucks -d aisucks -f /tmp/restore.sql
kubectl -n aisucks exec postgres-0 -- psql -U aisucks -t -c \
  'select count(*) from reports;'   # expect: the recorded number
```

Restart aisucks once so migrate-on-start reconciles against the restored
schema: `kubectl -n aisucks rollout restart deploy/aisucks`.

## 4. Gate

The release-runbook gate battery, plus the Cilium-specific checks:

```sh
H=https://$DOMAIN
curl -fsS -o /dev/null -w 'healthz %{http_code} in %{time_total}s\n' $H/healthz   # 200
curl -fsS $H/ | grep -q 'never be sold' && echo page ok
curl -s -o /dev/null -w 'garbage -> %{http_code}\n' -X POST -d 'link=https://evil.example/share/x' $H/report  # 422
kubectl -n kube-system exec ds/cilium -c cilium-agent -- cilium-dbg status --brief  # OK
kubectl -n kube-system exec ds/cilium -c cilium-agent -- cilium-dbg status --verbose | grep 'Socket LB'  # Enabled
for p in 9965 9964 4244 4240; do timeout 2 bash -c "</dev/tcp/$IP/$p" 2>/dev/null && echo "$p OPEN (BAD)" || echo "$p blocked"; done
kubectl get pods -A --no-headers | grep -vE 'Running|Completed' || echo all-running
```

Gamma additionally runs the canary submission. Watch the agent and
operator logs for Kubernetes API deprecation warnings (the k8s version can
run ahead of Cilium's tested matrix).

## 5. Rollback

`git revert` the version-bump commit, then repeat the wipe drill at the
reverted commit, restore the dump again. The dump from step 1 is the
invariant either way.

## Measured behavior to plan around

- Dump with `--clean --if-exists` (plain dumps emit benign schema-exists
  errors against migrate-on-start).
- The hubble-relay certgen round-trip is the recovery tail everywhere
  (~40–70s after the site is already serving); if the SLA must cover it,
  pre-mint via the certgen CronJob schedule.
- Reboot anatomy on these boxes: ~110s of firmware POST before Talos even
  starts — the 4-minute SLA spends nearly half its budget in the BIOS.

## Known gaps / follow-ups

- IMAGECACHE predates Cilium: the six quay.io images are cache misses on
  cold boot — converge depends on quay.io reachability. Refill the cache
  (`talosctl image cache-create` with the digest refs from
  cilium-inline.yaml) before relying on WAN-less cold boot.
- Hubble flow export stays OFF until an abuse/compliance ClickHouse domain
  exists for it (flows carry visitor IPs).
- Objects dropped by a re-render need manual pruning (Talos never deletes).
- Graceful reboots (talosctl reboot) leave Deployment pod corpses in
  phase=Failed that never GC (kube-controller-manager's
  terminated-pod-gc-threshold defaults to 12500 — never trips on a
  single-node fleet). Cosmetic, but they poison any "all pods Ready"
  check. Purge with `kubectl delete pods -A
  --field-selector=status.phase=Failed`; follow-up: set the KCM
  threshold low (~20) via machine config so the cluster self-cleans.
