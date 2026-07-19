# Admission doctrine

Policy about what may run in the cluster is encoded **once**, as admission
policy, and enforced twice: live by the apiserver at admission, and at PR time
by a generic harness that evaluates every policy against every rendered
manifest. Repository tests do not restate what manifests say; they exist only
where admission structurally cannot see the invariant.

## The three lanes

**1. Admission doctrine — `ValidatingAdmissionPolicy` / `MutatingAdmissionPolicy`.**
Any rule about a *single object* belongs here: safety floors, network
boundaries, alerting hygiene, scheduling defaults, secret hygiene. VAP states
what must be true; MAP makes it true when a manifest doesn't say otherwise.
Policies live in `src/infrastructure/base/admission/` (or next to the
component that owns them, e.g. the SpiceDB postgres topology policy) and are
applied by Flux like everything else. MutatingAdmissionPolicy is GA on the
management cluster; both policy kinds are served at
`admissionregistration.k8s.io/v1`.

**2. Combination invariants — Go tests in `src/infrastructure/tests/`.**
Admission sees one object per request. A test earns its existence only by
joining things admission cannot: release-manifest digest sets against every
rendered workload, version-skew coupling across lockfiles, Flux dependency
graphs, client/server transport pairings, capacity budgets justified by a
sizing doc. Every surviving test states the cross-resource rationale in its
doc comment. If the rationale names only one object, it is in the wrong lane.

**3. Artifact conformance — targeted tests for non-Kubernetes artifacts.**
SQL DDL drift, image-lock derivations, load-test script hygiene, Bazel module
pins. Same bar as lane 2: consistency *between* artifacts, not mirrors of one.

## What is banned

- **Statement-level mirroring.** A test that asserts a manifest's literal
  values (replica counts, resource sizes, alert lists copied from upstream
  charts, CIDR tables, embedded script text) encodes the manifest twice and
  freezes today's implementation as if it were doctrine. Delete on sight;
  if a generalizable rule is buried inside, promote the rule to a policy.
- **Policy-text tests.** Never assert the YAML of a VAP/MAP or re-implement
  its expression in Go. The harness compiles and evaluates the real thing.
- **Migration freezes.** Tests that assert files or objects *no longer exist*
  outlive their purpose the moment they first pass on main. Git history is
  the record of what was removed.
- **Merge gates encoded as failing tests.** A red test on main is never a
  coordination mechanism.

## The harness

`src/infrastructure/tests/admission_policy_harness_test.go` discovers every
VAP/MAP across the rendered manifest tree and, using the apiserver's own
compiler and patch code (`k8s.io/apiserver/pkg/admission/plugin/...`):

1. **Compiles every expression.** A CEL type error in a `failurePolicy: Fail`
   policy is a standing outage (or a platform-agent lockout); it fails CI
   before it can fail the cluster.
2. **Evaluates every policy against every rendered manifest** that matches
   its rules, bindings, and match conditions — so a manifest that would be
   denied by Flux apply is denied at PR time, with the policy's own message.
3. **Applies every mutation** to matching manifests and asserts it evaluates
   cleanly, is idempotent (mutating the mutated object is a no-op), and that
   the mutated result still passes every validating policy.
4. **Runs declarative fixtures** from `src/infrastructure/tests/fixtures/admission/`
   for request-shaped policies (identity, subresource, operation carve-outs)
   whose behavior can't be derived from manifests. Fixtures are YAML
   allow/deny tables, not Go.

Known limits, on purpose: params resolve only through the repo's
`configMapGenerator` sources; namespace selectors resolve only over
`kubernetes.io/metadata.name`; objects whose namespace is injected at apply
time are evaluated against unscoped policies only. The harness logs what it
skips. The apiserver remains the authority; the harness is preflight, never
a second control plane.

## Retirement roadmap

Phase 1 (this document): harness + first MAP (workload priority floor) +
deletion of pure-mirror and policy-text tests.

Phase 2 — promote buried rules to VAPs, then delete their host tests:
alert severities must be Alerta-deliverable and operator-owned
PrometheusRule sources declare no inline rules; no `cluster.local` outside
cert-manager `dnsNames`; no CiliumNetworkPolicy selecting the hostNetwork
ingress by pod label; no `0.0.0.0/0` on public-edge policies; no ClusterRole
granting `secrets`; no live/test payment credentials or private-key material
in manifests; every PVC uses an encrypted StorageClass; ExternalSecrets read
only their own namespace subtree.

Phase 3 — omnibus surgery: `openbao_conformance_test.go`,
`spicedb_conformance_test.go`, `tigerbeetle_conformance_test.go`,
`payments_conformance_test.go`, `edge_hardening_test.go`,
`vlogs_hardening_test.go`, `log_*_test.go`, `alert_tuning_test.go` each
shrink to their combination-invariant core; everything else converts or dies.
Shared YAML helpers move to a neutral file before `openbao_conformance_test.go`
shrinks.

Phase 4 — widen the workload-priority binding from the canary namespaces to
all Guardian namespaces once prod has observed a full promotion cycle, and
opt critical data planes (`guardian-critical`) up explicitly in their
manifests.
