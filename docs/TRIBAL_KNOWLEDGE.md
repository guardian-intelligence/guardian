# Tribal knowledge

Hard-won facts that are not obvious from the code, split into per-topic docs.
Most development is product work that needs almost none of the substrate lore:
read `docs/engineering-rules.md`, then only what your task routes to below.

## Route by task

- **Every change** — `docs/engineering-rules.md` (repo-wide rules and
  service/API conventions).
- **Wake Up, Mythra! (WUM)** — most development happens here. Read
  `docs/wake-up-mythra-development.md` (plan of record and architecture
  invariants), `src/chunkies/README.md` (one-command local loop), and
  `docs/netcode.md` (server-authoritative simulation contract). Gating client
  behavior: `docs/feature-flags.md`. Shipping a user-visible feature:
  `docs/canaries.md` and `docs/loadtest.md`. Following a user session across
  the stack: `docs/trace-correlation.md`. The substrate docs below matter
  only when deploying or operating the chunkies services in the cluster.
- **Postflight** — `docs/postflight-architecture.md` is the entry point and
  names its companions
  (`postflight-{product,fleet,host,scheduling,storage,security-model,runner-lifecycle,lightning,cli-distribution}.md`).
- **Company site, letters, PrivateCut** — `docs/letters-cms.md` (letters live
  in Directus, not Git), `docs/trace-correlation.md`, `docs/canaries.md`.
- **Identity and sign-in** — `docs/sign-in-with-guardian.md`;
  `docs/github-apps.md` for the first-party App inventory.
- **Payments and billing** — `docs/payments.md`, `docs/tigerbeetle.md`,
  `docs/tigerbeetle-financial-model.md`.
- **Secrets** — `docs/secrets.md`; it routes to `docs/openbao-design.md` and
  `docs/openbao-residue-inventory.md`.
- **Dependencies and supply chain** — `docs/dependency-management.md`,
  `docs/supply-chain-design.md`, `docs/registry-design.md`.
- **Cluster access from an agent environment** —
  `docs/agent-environment-authentication.md` (routes to the hosted-agent
  variants).
- **Cluster and infrastructure work** — `docs/cluster-architecture.md`
  (tenancy, promotion, edge, build/image inventory), plus
  `docs/admission-doctrine.md`, `docs/tofu-gitops-design.md`,
  `docs/management-cluster-trusted-boot-and-storage.md`, and
  `docs/reliability-rto.md`.
- **Operating the cluster from a workstation** —
  `docs/operator-workstation.md` (Talos access, personas, node-config regen,
  drills).
- **Bootstrap and disaster recovery** — `docs/bootstrap-dr.md`; the custody
  and cold-boot details live in `docs/secrets.md`.
- **Company strategy and context** — `docs/company-context.md`.
