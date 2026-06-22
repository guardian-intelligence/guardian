# Harbor publication substrate status

Date: 2026-06-22

## Scope

Components:

- Harbor `tenant-root/oci`.
- Company-site OCI artifact `oci.guardianintelligence.org/guardian/company-site`.

Desired state sources:

- `src/infrastructure/base/apps/harbor.yaml`.
- `src/products/company/site/BUILD.bazel`.
- `src/infrastructure/base/products/company-site.yaml`.

## Declared State

- Harbor is declared as `Harbor/tenant-root/oci` with host
  `oci.guardianintelligence.org`.
- The company-site image is built by `//src/products/company/site:image`.
- The Kubernetes deployment references the immutable digest
  `sha256:708390f2a646b7286fdc29c6d9bc0cc789932aa7ae6fa899ce436084e5435277`.
- `//src/products/company/site:push-harbor` publishes the image to
  `oci.guardianintelligence.org/guardian/company-site` using the repo-pinned
  `rules_oci` push toolchain.
- `aspect infra publish-company-site` is the operator command for the live push.

## Current Evidence

- The push target builds locally without contacting Harbor.
- `aspect infra preflight` includes the top-level build graph and therefore
  catches breakage in the company-site publish target.

## Not Yet Passed

- Harbor has not been converged in the live `guardian-mgmt` cluster from this
  workspace.
- No live `aspect infra publish-company-site` run has succeeded.
- No digest-addressed pull from Harbor has been verified.
- OCI auth delivery is still secret-zero/OpenBao session material, not checked
  into git.
