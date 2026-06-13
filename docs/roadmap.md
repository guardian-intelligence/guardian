# Engineering roadmap: finishing Phase 1

Status: 2026-06-12, ratified direction. Monitoring, releases, and platform
over product (operator decision; product capability already proven at
verself). Each milestone names its verification — the brick isn't laid until
the check passes. Specs referenced:
`docs/architecture/{metrics,slo,observability,gateway,topology}.md`,
`docs/runbooks/aisucks-release.md`.

## Dependency shape

```
M1 instruments (done) ──► M2 SLO layer ──► M6 rollout judgment
T fleet topology (3+2 clusters) ──► M3 prod conversion
M3 gateway (keystone) ──► M4 status v0
                     ├──► M7 public registry / Phase-1 exit
                     ├──► app egress lockdown, replicas:2
                     └──► (with M5) weighted canaries
M5 ledger (independent track)
M8 paved-road proof (capstone, needs M1–M7)
```

## M0 — survival floor

DONE. Cluster CA roots + prod corpus live age-encrypted in R2
`guardian-vault`; restore drill passed. Procedure, auth matrix, and record:
`docs/runbooks/survival-floor.md` (one operator action remains there: the
MacBook second copy).

## M1 — instruments

DONE. App RED + funnel + dependency + pool metrics, Converged events,
deploy dashboard, AppErrorRate/UpstreamFetchDegraded rules — spec and
verification clauses: `docs/architecture/metrics.md`.

## M2 — SLO layer (ratify + rules; needs days of M1 data)

Targets RATIFIED 2026-06-12 (99.9% avail / 99% <500ms / 99.5% submit —
slo.md). Recording rules compute SLI ratios + budget
remaining; burn-rate alerts replace crude thresholds when data matures.
VERIFY: budget arithmetic spot-checked against a known induced outage
window; rules load clean; numbers stable across a week.

## M3 — the Gateway keystone (design ratified; dev pilot next)

Five dependents (registry, status listener, app egress lockdown,
replicas:2, weighted canaries). Design: `docs/architecture/gateway.md` —
Cilium Gateway API in hostNetwork mode on the existing node Envoy; TLS
passthrough default (TLSRoute is GA since Gateway API v1.5; pin the CRD
bundle Cilium 1.19.4 is tested against), termination available
per-hostname once M7 provides key custody; conversion is live-apply +
SO_REUSEPORT listener handover, with the drilled wipe as fallback. Order:
dev pilot → gamma with the release gate run repeatedly through Envoy →
prod, where the conversion lands inside the topology migration
(`docs/architecture/topology.md`) — 3-node prod changes the ingress path
to a BGP floating IP, and prod edge surgery happens once, not twice.
VERIFY: dev passes the full release gate through Envoy; induced pod kill
shows the page's zero-downtime story intact; firewall posture unchanged
(admin plane untouched — CNPs never police 6443/50000).
STATUS: Phases 0–3 DONE (2026-06-13). Dev is converted and serving through
the Gateway: Envoy owns :80/:443, aisucks pod-network replicas:2 behind the
TLSRoute, per-pod scrape identity in VM, no-SNI drops at the edge, firewall
posture byte-identical, wipe drill converged the converted state from
bootstrap unattended (guardian up 129s; serving SLA missed at +621s due to
an ACME failed-authorization 429 inherited from the pilot's mis-ordered
flip — see the dated pilot record in gateway.md for the numbers, the
arm-then-flip ordering rule, the pod→gateway hairpin finding + Gatus
probe-alias fix, and the pre-wipe cert-cache backup rule). Open for gamma
(Phase 4): the SO_REUSEPORT overlap measurement (never coexisted on dev),
forced ACME renewal through passthrough, and the ~2-request pod-kill drop
window (SIGTERM listener-close vs endpoint-removal race) before "zero-drop"
can be claimed.

## M4 — status v0 (after M3; non-critical by declaration)

`src/status/` Go module + `apps/status-web` (TanStack, embedded via the
emit-static pipeline): VM-backed (sibling probe_success + SLO rules),
15–45s freshness, served from dev first, multi-A later. NOT a product: no
SLO on the page itself; if it dies, the operator sends an email. The
millisecond live tier stays deferred (slo.md phasing).
VERIFY: induced gamma outage visible on the page at scrape cadence;
release-train section shows a real promotion as it happens.

## M5 — the ledger release (independent track)

Per-site ClickHouse (component authored; add to push.go), filelog pipeline
(container parser, no k8sattributes), OTLP receiver on loopback + app OTel
SDK (traces; slog↔trace correlation), k8sobjects capturing Events (etcd
forgets in 1h; the ledger remembers), R2 backups as CronJobs (pg + CH) with
a RESTORE DRILL on gamma, dead-man heartbeat (Watchdog rule riding the data
path + Gatus probing Alertmanager for its presence) — Gatus narrows to
meta-monitoring, stops double-paging. PII redaction stays deferred under
the source-discipline rule (no new log emitter without a what-it-logs
review; pgx extended protocol keeps statements parameter-free).
VERIFY: application trace visible end-to-end in CH with its log lines by
trace ID; restore drill produces a counted, queryable corpus copy; killing
vmalert pages via the dead-man within its window.
STATUS 2026-06-12: first two sub-items landed on dev+gamma — per-site
ClickHouse in push.go behind the site-gated `clickhouse.enabled` flag (OFF
on prod until its clickhouse-admin Secret exists) and the filelog +
k8sobjects Events pipeline (docs/runbooks/ledger.md). OTLP/app SDK, R2
backups + restore drill, and the dead-man heartbeat remain open; M5 is NOT
done.

## M6 — rollout judgment (needs M1–M2; design: docs/architecture/release.md)

Ratified 2026-06-12: build/deploy authority split. GitHub builds, signs
provenance keyless, pushes to ghcr, advances the edge channel — and holds
zero cluster credentials; per-cluster **Flux** (source + kustomize
controllers) pulls, verifies, applies; a small **release judge** runs the
gamma soak (alerts quiet + probes clean + restart delta zero + 5xx zero
over 10m, queried from VM), Transit-signs the gate-pass, advances stable;
on prod it admits (provenance ∧ gate-pass ∧ ¬tainted ∧ budget), watches
15m post-promote, and rolls back by pointer move — fail-open on missing
telemetry (operator amendment). The self-hosted runner POC retires. The
Connect Health probe becomes the first release-judge synthetic and later
expands to product-write drills once the database/verifier slice exists;
budget gate lands (M2's error budget exhausted → feature releases freeze,
reliability fixes only).
VERIFY: a deliberately broken release (failing synthetic) is refused at
gamma; a synthetic budget exhaustion blocks a feature release; auto-rollback
drill on gamma restores N-1 unattended.

## M7 — provenance + public vending (Phase-1 exit gate)

Signing split per docs/architecture/release.md: build provenance is cosign
**keyless** (GitHub OIDC, identity-pinned) — no long-lived key in CI; bao
Transit (init on gamma first, the gate's signer) signs the fleet artifacts:
gate verdicts, the stable pointer, deployed attestations. in-toto
SLSA-provenance-v1 per pushed digest; the CUE release manifest (release →
component digests → commit, signed; status page data source; channels
stable/edge as signed pointers). Vending after M3: zot (no Harbor) behind
the Gateway at oci.guardianintelligence.org (domain spelling .org vs .com
to be settled in AGENTS.md), publishing images + attestations + manifests;
zot also serves each site as a pull-through mirror. Reproducibility remains the backstop: anyone rebuilds the commit
and matches the digest — we already prove this on every release.
VERIFY: `cosign verify` documented and passing from a machine that has only
the public key and the registry URL; a third party can rebuild and match a
digest following only public docs. THIS IS THE PHASE-1 EXIT.
STATUS 2026-06-13: first public-vending bridge was proven for aisucks. A
GitHub-hosted tag workflow proved `ghcr.io/guardian-intelligence/aisucks`
pushes, keyless signing, and SLSA/in-toto provenance; that workflow bridge has
now been removed in favor of repo-owned release binaries executed through
`aspect`. The npm SDK has a separate Trusted Publishing lane with repo-owned
publish/no-op logic. Remaining before M7 is done: trusted-publisher
verification, release manifest/channel artifacts, gate-pass attestations, and
clean-machine verification of a real public digest.

## M8 — the paved-road proof (capstone)

Make "perfect service slice" literal: add a trivial service through the
conventions — Protobuf/Connect contract, component entry, manifest. It
must inherit scrape, RED dashboard, SLO rows, deploy markers, gated
release, rollback, status presence, signed provenance, with near-zero
bespoke wiring. The diff IS the measure: if the service needs more than a
contract + a manifest + a components entry, the road isn't paved — fix the
platform, not the tenant.
VERIFY: the service ships through the full pipeline to prod and appears
everywhere a service should, then is removed as cleanly (yank drill).

## Standing items outside the milestones

- Cred custody system (Zitadel + SpiceDB era; M0 is the floor until then).
- Prod billing thread (operator-owned).
- ntfy topic rotation before the repo goes public.
- Hubble flow export stays off until an abuse/compliance CH domain exists.
- Verself subsumption + workload plane (QEMU warm pools, the workload
  agent): direction in AGENTS.md "Compute doctrine" and
  `docs/architecture/topology.md`; per-box wipes wait on explicit operator
  go. First verification drill: a 1000-microVM burst stress test across
  workload capacity — its own workstream, designed separately (never on
  the prod box).
