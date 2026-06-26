# Temporary Flux handoff path

Flux currently reads the company production deployment from this path on
`main`. Keep this compatibility copy until the live `guardian-company-prod`
Kustomization points at
`src/infrastructure/clusters/ash/deployments/company/prod`.

Do not add new deployment changes here. The canonical ASH company prod overlay
lives in `src/infrastructure/clusters/ash/deployments/company/prod`.
