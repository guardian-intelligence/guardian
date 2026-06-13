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

The next implementation PR should deliver the **Runtime slice**: serve Connect
Health publicly while keeping `/healthz` and `/livez` as raw operational
endpoints.

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

- [ ] npm package tarball is built from repo source.
- [ ] Package integrity is recorded.
- [ ] Package is published to npm with Trusted Publishing provenance.
- [ ] `edge` dist-tag points at the intended SDK version.
- [ ] Package contents contain generated Connect client only for Health.

### OCI Distribution

- [ ] zot stores release artifacts by immutable digest.
- [ ] zot serves OCI Distribution v1.1 referrers.
- [ ] Each release target has subject digest plus referrers:
  - [ ] cosign/keyless or Transit signature
  - [ ] SLSA provenance
  - [ ] in-toto statement
  - [ ] SBOM, even minimal at first
  - [ ] release manifest / metadata
  - [ ] gate result
- [ ] Public reads are digest-addressed; mutable tags are channel convenience
  only.

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
  - [ ] `aisucks-ts-sdk / any / default / npm-public / edge`

### Build Provenance

- [ ] SLSA provenance names:
  - [ ] source repository
  - [ ] source commit
  - [ ] Bazel target
  - [ ] builder identity
  - [ ] build type
  - [ ] parameters: distributable, platform, flavor, publisher, channel
- [ ] Provenance subject digest matches the published artifact digest or npm
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
- [ ] Connect Health is publicly reachable.

### Synthetic Result

- [ ] Repo-owned synthetic installs `@guardian-intelligence/aisucks@edge`.
- [ ] Synthetic calls Health against gamma and prod.
- [ ] Synthetic emits `synthetic-result.v1` JSON:
  - [ ] package/version installed
  - [ ] endpoint URL
  - [ ] operation full name
  - [ ] status
  - [ ] latency
  - [ ] timestamp
  - [ ] source commit or release manifest digest

### SLO Gate Result

- [ ] Gate evaluator queries simple SLOs:
  - [ ] synthetic success
  - [ ] app 5xx == 0 over window
  - [ ] pod restart delta == 0 over window
  - [ ] health/liveness probe success
- [ ] Gate evaluator emits `gate-result.v1` JSON:
  - [ ] candidate digest or manifest digest
  - [ ] decision pass/fail
  - [ ] checked queries
  - [ ] observed values
  - [ ] time window
- [ ] Gate result is later signed as an in-toto attestation.

### Promotion / Channel Pointer

- [ ] `edge` can point at every main candidate.
- [ ] `nightly` is promoted only after gate passes.
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
cosign verify <zot-or-ghcr-image>@sha256:...
cosign verify-attestation --type slsaprovenance <image>@sha256:...
npm view @guardian-intelligence/aisucks@edge dist.integrity
npm install @guardian-intelligence/aisucks@edge
guardian/repo tool verify release-manifest <digest-or-file>
guardian/repo tool synthetic health --base-url=https://gamma.aisucks.app
```

## Minimum Full Slice

The smallest meaningful release slice is:

1. Proto Health.
2. Generated Go service + generated TS SDK.
3. API image pushed to zot by digest.
4. SDK published to npm edge.
5. SLSA/in-toto provenance attached to the API image.
6. Release manifest records image digest + npm package integrity.
7. Synthetic installs npm edge and calls gamma Health.
8. Gate result JSON emitted.
9. `nightly` pointer advances only after gate pass.

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
aspect release npm-sdk
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
2. SDK edge slice: publish the generated Health SDK to npm `edge`.
3. Synthetic slice: install npm `edge` and call gamma/prod Health.
4. Gate slice: emit `synthetic-result.v1` and `gate-result.v1`.
5. Release manifest slice: record API image digest + npm integrity in a
   machine-readable manifest.
6. Distribution slice: attach provenance/SBOM/gate referrers and advance signed
   channel pointers.
