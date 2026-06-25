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

## Commands

Use a Kubernetes API server that is not the node being rebooted. Reports are
temporary drill evidence; write them under `/tmp` unless a PR explicitly asks for
checked-in evidence.

```sh
aspect infra edge-failover-drill \
  --kubeconfig=src/infrastructure/talm/kubeconfig \
  --node-name=ash-earth \
  --node-ip=206.223.228.101 \
  --confirm-node-ip=206.223.228.101 \
  --kube-api-server=45.250.254.119:6443 \
  --report=/tmp/guardian-edge-failover-ash-earth.json

aspect infra edge-failover-drill \
  --kubeconfig=src/infrastructure/talm/kubeconfig \
  --node-name=ash-wind \
  --node-ip=45.250.254.119 \
  --confirm-node-ip=45.250.254.119 \
  --kube-api-server=206.223.228.101:6443 \
  --report=/tmp/guardian-edge-failover-ash-wind.json

aspect infra edge-failover-drill \
  --kubeconfig=src/infrastructure/talm/kubeconfig \
  --node-name=ash-water \
  --node-ip=206.223.228.87 \
  --confirm-node-ip=206.223.228.87 \
  --kube-api-server=206.223.228.101:6443 \
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
