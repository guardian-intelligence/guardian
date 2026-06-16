# Release Verification Todo

This document is the working checklist for the aisucks Health vertical slice and
the release system around it. The list is intentionally artifact-shaped: every
item should eventually have a command, digest, integrity string, manifest entry,
or recorded result that a clean machine can verify.

## Current Focus

The Contract slice is delivered by the Connect/RPC Health contract work:

- Connect/RPC-first `AisucksService.Health`.
- Operation policy attached to the operation contract.
- Reproducible Go and TypeScript generated surfaces from repo tooling.
- SDK source surface exposes only `health()`.
- Buf lint and breaking-change checks run through Bazel with a pinned local
  toolchain and no remote plugins.

The SDK release slice is delivered by `aspect release sdk-oci`, which delegates
to the package-owned Effect/TypeScript release state machine under
`src/viteplus-monorepo/packages/aisucks-sdk/release/`. Check mode builds the
generated Health SDK tarball, writes it as an OCI artifact subject in a local
OCI layout, validates admission before any public write, and records the event
log in `release-result.json`.

The current release-system work makes live public publish, npm dist-tags, and
Health gates permutations of the same package-owned release logic.

## Todo

### Contract

- [x] `aisucks.proto` defines only `AisucksService.Health`.
- [x] Operation policy is attached or generated:
  - [x] auth requirement
  - [x] audit level
  - [x] risk tier
  - [x] request body limit
  - [x] rate limit
  - [x] idempotency requirement
- [x] Generated Go server/client bindings are reproducible from repo tooling.
- [x] Generated TypeScript SDK surface exposes only `health()`.
- [x] Buf lint runs through Bazel with the repo-pinned `rules_buf` toolchain.
- [x] Buf breaking-change detection compares against a checked-in baseline
  image.
- [x] Protobuf generation/check wiring is hidden behind repo Starlark wrappers,
  not product-owned raw `protoc` shell commands.

Verification:

```sh
bazelisk test //src/products/aisucks/api:buf_lint_test //src/products/aisucks/api:buf_breaking_test
bazelisk build //src/products/aisucks/api:ts_sdk_codegen_check
```

Baseline refresh command, for intentional public API changes:

```sh
bazelisk run @rules_buf_toolchains//:buf -- build -o src/products/aisucks/api/testdata/buf/aisucks-api.binpb .
```

### Service Artifact

- [ ] Bazel builds the aisucks API OCI image.
- [ ] Image is pushed by digest to internal registry/zot.
- [ ] Image has OCI annotations for:
  - [ ] source repository
  - [ ] source commit
  - [ ] distributable
  - [ ] platform
  - [ ] flavor
  - [ ] version or channel
- [ ] Image digest is recorded in a release manifest.

### SDK Artifact

- [x] Package-owned `aspect release sdk-oci` writes the npm package tarball as
  an OCI artifact subject in a declared local OCI layout.
- [ ] npm package tarball is pushed to the public OCI registry by digest.
- [x] Package integrity is recorded.
- [x] npm publication is implemented as a downstream projection from the verified
  OCI subject.
- [ ] Package is published to npm through the GitHub executor shim.
- [ ] `edge` dist-tag points at the intended SDK version.
- [ ] `nightly` and `rc` dist-tags move only after matching gate passes.
- [x] Package contents contain generated Connect client only for Health.

### GitHub Release Assets

- [ ] GitHub Release asset projection is owned by the distributable release
  tool, not by the Guardian CLI or repo-wide release code.
- [ ] Each uploaded asset has a digest in a signed release manifest.
- [ ] Each uploaded executable/package-like asset has DSSE/in-toto SLSA
  provenance and the full Sigstore bundle available beside the asset.
- [ ] The GitHub Release body links verification commands and the release
  manifest digest instead of treating the GitHub Release page as the ledger.

### OCI Distribution

- [x] Platform TLS for `oci.guardianintelligence.org` is solved through
  Cilium Gateway termination with product TLS passthrough preserved.
  Crossplane owns the `EdgeGateway` platform substrate; each enabled site
  declares its concrete `EdgeGateway` object in checked-in site manifests.
- [ ] zot stores release artifacts by immutable digest.
- [ ] zot serves OCI Distribution v1.1 referrers.
- [ ] Each release target has subject digest plus referrers:
  - [ ] cosign/keyless or Transit signature
  - [ ] SLSA provenance
  - [ ] in-toto statement
  - [ ] DSSE JSONL bundle
  - [ ] SBOM, even minimal at first
  - [ ] release manifest / metadata
  - [ ] gate result
- [ ] Public reads are digest-addressed; mutable tags are channel convenience
  only.
- [x] SDK can be pulled from the local OCI layout with
  `guardian run oras pull --oci-layout /tmp/guardian-sdk-release/oci-layout:edge -o ./dist`.
- [ ] SDK can be pulled from the public OCI registry with
  `guardian run oras pull oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>`.

### Release Tuple Manifest

- [ ] Emit a machine-readable manifest, likely CUE-rendered JSON, with all
  release targets.
- [ ] For each permutation, record:
  - [ ] distributable
  - [ ] source commit
  - [ ] external version or coordinate
  - [ ] publisher
  - [ ] platform
  - [ ] flavor
  - [ ] channel
  - [ ] artifact digest or package integrity
- [ ] Example targets for this slice are represented:
  - [ ] `aisucks-api-image / linux-amd64 / default / zot-internal / edge`
  - [ ] `aisucks-api-image / linux-amd64 / default / ghcr-public / edge`
  - [ ] `aisucks-ts-sdk / any / default / oci-public / edge`
  - [ ] `aisucks-ts-sdk / any / default / npm-public / edge`

### SLSA

- [ ] Guardian release evidence schemas live in
  `src/release/evidence/schemas.cue`. They define the Guardian-owned contract
  around standards-owned in-toto/SLSA predicates before verifier code exists.
- [ ] Every Guardian VSA uses `predicateType:
  https://slsa.dev/verification_summary/v1`.
- [ ] DSSE envelopes use `payloadType: application/vnd.in-toto+json`, base64
  payload bytes, and at least one signature. Sigstore bundle verification
  material remains tool-owned, not re-modeled by Guardian.
- [ ] Every Guardian VSA carries `subject` digests, `resourceUri`, verifier
  identity, policy URI + digest, input attestation digests, `PASSED`/`FAILED`,
  verified levels, and the Guardian extension field
  `https://guardianintelligence.org/evidence/v1`.
- [ ] Minimal Guardian VSA policy surfaces are represented. The VSA `kind`
  names the policy surface; `verificationResult` records pass/fail.
  - [ ] `build`
  - [ ] `license`
  - [ ] `promotion` with `track: nightly | rc | stable`
  - [ ] `deployment`

### Build Provenance

- [ ] SLSA provenance names:
  - [ ] source repository
  - [ ] source commit
  - [ ] Bazel target
  - [ ] builder identity
  - [ ] build type
  - [ ] parameters: distributable, package, version, channel, OCI ref
- [ ] Provenance subject digest matches the admitted SDK OCI digest and npm
  integrity.

### Release Notes

- [x] Changesets produces package changelog for SDK changes.
- [ ] Release command extracts notes for the SDK release target.
- [ ] Manifest links notes to the exact npm package/version.

### Runtime Deployment

- [ ] gamma runs the exact aisucks API image digest selected by the release
  manifest.
- [ ] prod either remains on the previous digest or receives promotion after
  gate.
- [ ] `/healthz` and `/livez` remain raw operational endpoints.
- [x] Connect Health is served by the API binary.
- [ ] Connect Health is publicly reachable on gamma.

### Synthetic Result

- [x] Repo-owned synthetic installs `@guardian-intelligence/aisucks@<channel>`.
- [x] Synthetic calls Connect Health against the selected endpoint.
- [x] Synthetic emits `synthetic-result.v1` JSON:
  - [x] package/version installed
  - [x] endpoint URL
  - [x] operation full name
  - [x] status
  - [x] latency
  - [x] timestamp
  - [ ] source commit or release manifest digest

### SLO Gate Result

- [x] Gate evaluator checks simple SLOs:
  - [x] synthetic success
  - [x] required Health capability
  - [x] p95 latency
  - [x] observed TPS
  - [x] package tarball/unpacked size
  - [ ] app 5xx == 0 over window
  - [ ] pod restart delta == 0 over window
  - [ ] health/liveness probe success
- [x] Gate evaluator emits `gate-result.v1` JSON:
  - [ ] candidate digest or manifest digest
  - [x] decision pass/fail
  - [x] checked queries
  - [x] observed values
  - [ ] time window
- [x] Gate evaluator emits a SLSA VSA statement plus DSSE JSONL for the
  promotion verdict.
- [ ] Gate VSA is attached as an OCI referrer on the promoted release subject.

### Promotion / Channel Pointer

- [ ] `edge` can point at every main candidate.
- [x] npm `nightly` and `rc` dist-tags can be promoted by the workflow only
  after the gate step passes when `promote-on-pass` is selected.
- [ ] Channel pointer is a signed object, not just a mutable tag.
- [ ] Pointer records whether promotion used the same digest or
  package-specific rebuild lineage.

### Git Tag

- [ ] RC/stable tags come first; edge/nightly tags are optional.
- [ ] Annotated tag points to a release manifest digest.
- [ ] Tag message includes distributable/version/source commit and artifact
  digest(s).
- [ ] Tag creation happens after publish/gate success.

### Observability

- [ ] Connect Health has metrics/traces with operation name.
- [ ] Request logs/traces include release target/digest labels where feasible.
- [ ] Audit event exists for release publish, synthetic, gate decision, and
  promotion.

### Verification Commands

A clean machine should eventually be able to run:

```sh
guardian run cosign verify <zot-or-ghcr-image>@sha256:...
guardian run cosign verify-attestation --type slsaprovenance <image>@sha256:...
npm view @guardian-intelligence/aisucks@edge dist.integrity
npm install @guardian-intelligence/aisucks@edge
aspect release sdk-oci --output-dir /tmp/guardian-sdk-release
guardian run oras pull --oci-layout /tmp/guardian-sdk-release/oci-layout:edge -o ./dist
guardian run oras discover --oci-layout /tmp/guardian-sdk-release/oci-layout:edge
guardian run oras pull oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest> -o ./dist
guardian run cosign verify oci.guardianintelligence.org/guardian/aisucks/sdk/npm@sha256:<manifest>
guardian/repo tool verify release-manifest <digest-or-file>
guardian/repo tool synthetic health --base-url=https://gamma.aisucks.app
```

## Minimum Full Slice

The smallest meaningful release slice is:

1. Proto Health.
2. Generated Go service + generated TS SDK.
3. API image pushed to zot by digest.
4. SDK tarball pushed to OCI as `guardian/aisucks/sdk/npm` by digest.
5. SDK published to npm edge from the verified OCI subject.
6. Release manifest records image digest, SDK OCI digest, and npm package
   integrity.
7. Synthetic installs npm edge and calls gamma Health.
8. Gate result JSON emitted.
9. `nightly` pointer advances only after gate pass.

SLSA/in-toto provenance and signatures are the next evidence layer, not part
of this milestone's minimum slice.

## First Deliverable: Contract Slice

The first PR-able implementation slice is the Contract checklist above. It is
small enough to review, but valuable because it fixes the public API source of
truth before runtime, release, and Crossplane layers depend on it.

### Implementation Strategy

1. Add a repo-pinned Protobuf/Connect generation path.
   - The repo currently has Go, Bazel, and the VitePlus TypeScript workspace,
     but no checked-in Protobuf/Connect generation stack.
   - Prefer Bazel-owned generation or a repo-pinned generator invoked through
     `aspect`; do not depend on host-global protoc, buf, npm binaries, or Go
     tools.
   - Buf is the schema governance tool: lint, format, and breaking checks run
     through Bazel with local pinned toolchains. Remote plugins are not used in
     build, test, or release paths.

2. Add Guardian operation policy options.
   - Define a small `guardian.policy.v1` Protobuf options package.
   - Start with the fields needed for Health: auth, audit level, risk tier,
     max request bytes, rate limit, and idempotency.
   - Keep the policy schema boring and generated into Go metadata before
     adding Rego/CEL.

3. Add the aisucks Health contract.
   - Put the public API under a stable package such as
     `guardian.products.aisucks.v1`.
   - Define only `AisucksService.Health`.
   - Keep `/healthz` and `/livez` out of the public SDK contract; they remain
     raw operational endpoints.

4. Generate Go surfaces.
   - Generate the service interface, handler registration, and client types.
   - Export enough Go metadata for operation policy lookup by Connect full
     method name.
   - Do not wire the runtime handler in this first slice unless generation
     requires a compile-time integration point.

5. Generate TypeScript SDK surfaces.
   - Add the Connect runtime dependencies through the existing
     `src/viteplus-monorepo` package catalog.
   - Generate or wrap the generated client so the public SDK exposes only
     `health()`.
   - Remove the handwritten legacy public SDK API and old service endpoint in
     this slice.

6. Add deterministic build checks.
   - Add Bazel targets that fail if generated outputs drift from the contract.
   - No local runtime tests are required for this slice per operator direction.
   - Required verification should be build/generation-oriented, for example:

```sh
bazelisk build //src/products/aisucks/api:all
bazelisk build //src/viteplus-monorepo:workspace_build
vp run -w lint
```

### Acceptance Criteria

- `aisucks.proto` contains only `AisucksService.Health`.
- Health has explicit operation policy metadata.
- Go and TypeScript generated surfaces are produced by repo tooling.
- `@guardian-intelligence/aisucks` exposes `health()` as the intended SDK
  surface.
- No release workflow YAML is introduced.
- No fleet deploy, npm publish, Crossplane install, or SLO gate is required in
  this deliverable.

### Follow-On Deliverables

1. Runtime slice: serve Connect Health publicly while keeping `/healthz` and
   `/livez` raw.
2. Platform TLS slice: Cilium Gateway terminates TLS for
   `oci.guardianintelligence.org` from a cert-manager-managed Secret while
   product hostnames remain passthrough.
3. SDK OCI slice: build the generated Health SDK tarball and publish it as
   `guardian/aisucks/sdk/npm` by digest.
4. npm projection slice: publish the verified SDK OCI subject to npm `edge`.
5. Synthetic slice: install npm `edge` and call gamma/prod Health.
6. Gate slice: emit `synthetic-result.v1` and `gate-result.v1`.
7. Release manifest slice: record API image digest, SDK OCI digest, and npm
   integrity in a machine-readable manifest.
8. Distribution slice: attach provenance/SBOM/gate referrers and advance signed
   channel pointers.
