# Temporary Flux handoff path

Flux objects currently running in `guardian-mgmt-ash` still read this path from
`main`. Keep this compatibility copy until Flux has reconciled
`src/infrastructure/clusters/ash/root/flux/sync.yaml` and the live
Kustomizations point at `src/infrastructure/clusters/ash/root`.

Do not add new infrastructure here. The canonical ASH root lives in
`src/infrastructure/clusters/ash/root`.
