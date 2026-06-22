# management-inventory checks

Provider-free OpenTofu checks that prove duplicated management-cluster network
intent still matches `src/infrastructure/inventory/guardian-mgmt.json`.

Run through the repo-pinned OpenTofu binary:

```sh
aspect infra inventory-check
```

This root has no backend and no providers. It decodes checked-in JSON/YAML only,
then fails if the Talos VIP, Talm values, Cozystack platform external IPs,
MetalLB private pool, or kube-ovn MTU drift away from the inventory.
