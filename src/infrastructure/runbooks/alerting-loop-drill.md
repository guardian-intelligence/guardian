# Alerting-loop drill — prove alerts reach a human

The delivery chain under test:

```
VMRule → vmalert-shortterm → Alertmanager → Alerta (slack plugin, text-only)
      → alert-relay (tenant-root, Slack-format → ntfy) → ntfy topic
```

Flagger AlertProviders post to the relay directly (skipping the metrics leg);
the relay's dead-man polls Alertmanager for the always-firing `Watchdog`
alert and pages on silence, so "no news" is a detectable state, not a hope.

Everything below is copy-paste executable with the custody kubeconfig
(`KUBECONFIG=~/guardian-custody/kubeconfig-public`). Run it after any change
to the relay, the Monitoring app values, or the Kargo detection stack —
an alerting pipeline without a passing drill is assumed broken.

## Drill 1 — delivery leg (synthetic critical, ~1 min)

```sh
kubectl port-forward -n tenant-root svc/vmalertmanager-alertmanager 19095:9093 &
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ); END=$(date -u -d '+5 minutes' +%Y-%m-%dT%H:%M:%SZ)
curl -s -X POST 127.0.0.1:19095/api/v2/alerts -H 'Content-Type: application/json' -d "[{
  \"labels\":{\"alertname\":\"AlertingLoopDrillCritical\",\"severity\":\"critical\",
             \"namespace\":\"tenant-root\",\"service\":\"alert-relay\"},
  \"annotations\":{\"summary\":\"Live-fire drill: end-to-end delivery test\"},
  \"startsAt\":\"$NOW\",\"endsAt\":\"$END\"}]"
```

PASS = a notification on the ops ntfy topic within ~60s at **priority 5**
with tags `critical, production`. The alert self-expires in 5 minutes.

Machine check (topic URL is in OpenBao at
`kv/guardian/guardian-mgmt/tenant-root/alerting`, never in Git):

```sh
curl -s "$NTFY_TOPIC_URL/json?poll=1&since=5m" | grep AlertingLoopDrillCritical
```

## Drill 2 — Kargo detection leg (unhealthy warehouse, ~17 min)

Kargo's admission webhook refuses Promotions whose freight does not resolve
(the error is a misleading "Stage defines no promotion steps"), so promotion
failures cannot be injected by reference. The drillable failure — identical
in signal to expired GitHub credentials — is warehouse discovery:

```sh
cat <<EOF | kubectl create -f -
apiVersion: kargo.akuity.io/v1alpha1
kind: Warehouse
metadata:
  name: drill-warehouse-unhealthy
  namespace: guardian-iam
spec:
  interval: 1m0s
  subscriptions:
    - image:
        repoURL: ghcr.io/guardian-intelligence/nonexistent-drill-image
        semverConstraint: 1.x
EOF
```

Within ~90s the Warehouse reports `Healthy=False` ("DENIED: requested access
to the resource is denied"). `KargoWarehouseNotHealthy` has `for: 15m`, so
the page lands at ~16–18 min, priority 5.

**Cleanup is part of the drill:**

```sh
kubectl delete warehouse drill-warehouse-unhealthy -n guardian-iam
```

## Dead-man check (passive)

The relay exposes `relay_watchdog_last_seen_timestamp_seconds` and
`relay_pipeline_silent` on `/metrics` (scraped; `AlertRelayPipelineSilent`
mirrors it in-dashboard). If the Watchdog vanishes from Alertmanager for
10 minutes, both relay replicas page "alerting pipeline silent" directly —
expect a duplicate; two pages beat zero.

## Drill log

| Date | Drill | Result | Evidence |
|---|---|---|---|
| 2026-07-06 | Delivery (synthetic critical) | PASS | priority-5 page, tags critical/production; receipt confirmed by operator |
| 2026-07-06 | Kargo (unhealthy warehouse) | PASS | KargoWarehouseNotHealthy paged priority 5 at ~17 min; warehouse deleted |

## Known limits (stated, not hidden)

- Flagger→relay is schema-verified and network-verified but has no live-fire
  entry yet; the next real canary rollback (e.g. a Keycloak upgrade) is the
  drill. Add its row above when it happens.
- Alerta renders the alert `resource` as the scrape instance (`ip:port`) in
  page titles for rules without a resource-ish label; cosmetic, fix in the
  VMRule labels if it grates.
- The ntfy topic is an unauthenticated public topic; the relay is the single
  swap point when it moves to a reserved topic or self-hosted ntfy.
- No severity floor: informational alerts page too (deliberate, revisit in
  relay config when volume warrants).
