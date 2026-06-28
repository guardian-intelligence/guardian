# Edge Failover Drill

The edge failover drill validates the customer-facing SLA for one hard node
failure. Customer workloads must not be disrupted for more than 60 seconds when
any single management-cluster node fails.

Run this drill once per node. Document the recovery time for each node before
moving to the next node. Do not intentionally knock out more than one node at a
time.

## Preconditions

- Flux has reconciled the intended `main` revision.
- `aspect infra edge-health` passes.
- All three Kubernetes nodes are `Ready`.
- Ingress has at least one ready controller pod on each node.
- The probe URL has a high-availability backend. The default is
  `https://s3.guardianintelligence.org/`, backed by the Cozystack system
  SeaweedFS S3 deployment. Do not use the Cozystack dashboard as the default
  failover probe unless its chart exposes a durable HA setting; Cozystack 1.5.0
  and 1.5.1 hard-code the dashboard console and gatekeeper to one replica.

## Commands

Use the current Kubernetes API VIP (`https://10.8.0.250:6443`) while rebooting
individual nodes. Reports are temporary drill evidence; write them under `/tmp`
unless a PR explicitly asks for checked-in evidence.

The drill's k6 probe defaults to `ttl=0,select=random,policy=preferIPv6` so this
runner measures Cloudflare edge-to-origin failover without the local IPv4 egress
loss observed from the development host.

```sh
aspect infra edge-failover-drill \
  --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig \
  --node-name=ash-earth \
  --node-ip=206.223.228.101 \
  --confirm-node-ip=206.223.228.101 \
  --kube-api-server=https://10.8.0.250:6443 \
  --url=https://s3.guardianintelligence.org/ \
  --expected-statuses=200,403 \
  --report=/tmp/guardian-edge-failover-ash-earth.json

aspect infra edge-failover-drill \
  --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig \
  --node-name=ash-wind \
  --node-ip=45.250.254.119 \
  --confirm-node-ip=45.250.254.119 \
  --kube-api-server=https://10.8.0.250:6443 \
  --url=https://s3.guardianintelligence.org/ \
  --expected-statuses=200,403 \
  --report=/tmp/guardian-edge-failover-ash-wind.json

aspect infra edge-failover-drill \
  --kubeconfig=src/infrastructure/clusters/ash/bootstrap/talm/kubeconfig \
  --node-name=ash-water \
  --node-ip=206.223.228.87 \
  --confirm-node-ip=206.223.228.87 \
  --kube-api-server=https://10.8.0.250:6443 \
  --url=https://s3.guardianintelligence.org/ \
  --expected-statuses=200,403 \
  --report=/tmp/guardian-edge-failover-ash-water.json
```

After each run, wait for the node to return `Ready` and rerun
`aspect infra edge-health` before starting the next node.

## Pass Criteria

- `kubernetes_node_recovered` is `true`.
- `max_outage_ms` is less than `60000`.
- `aspect infra edge-health` passes after recovery.

If any node breaches the 60 second public-edge outage budget, stop the drill and
fix the load-bearing component before continuing. The purpose of the drill is to
prove that no single node is load bearing.
