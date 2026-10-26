# October 2026 dependency security preparation

These updates follow open advisories affecting the current committed lockfiles.
They retain the existing release lines. Dependencies moved by minimum-version
constraints are included; unrelated latest-version upgrades are excluded.

| Area | Prepared fix | Reason |
| --- | --- | --- |
| Bazel Go compiler | 1.26.5 to 1.26.10 | [October Go security releases](https://go.dev/doc/devel/release) fix HTTP/2 and crypto/x509 issues. The patched Hauler containerd also requires Go 1.26.8 or later. |
| Bazel and web runtime Node.js | 24.18.0 to 24.22.1 | [October Node.js security release](https://nodejs.org/en/blog/release/v24.22.1) fixes the eMudhra trust issue, TLS hostname validation and Brotli memory safety. Web image node layers consume this toolchain. |
| Shared image CA bundle | Dated curl/Mozilla snapshot 2026-10-20 | Replace the May snapshot shipped in all five web images and tofu-runner; upstream removes the affected emSign roots. Verify actual certificate fingerprints in the Bazel gate. |
| Hauler isolated module | containerd 2.3.6, chi 5.4.0, compress 1.18.7, gRPC 1.83.2 | Open advisories affect its old resolved dependencies. Preserve the Hauler 2.0.2 source pin, distribution replacement and isolation from the root module. Minimum-version selection also moves required patch dependencies. |
| Postflight CLI | Rustls 0.23.45 and rustls-webpki 0.103.16 | [GHSA-2mjx-qc3c-rqvc](https://github.com/rustls/rustls/security/advisories/GHSA-2mjx-qc3c-rqvc): TLS handshake encryption-level validation. |
| Benchmark Python environment | aiohttp 3.14.4, filelock 4.2.1, PyJWT 2.15.0, opentelemetry-api 1.45.1, mcp 1.30.0, multidict 6.9.1, urllib3 2.8.0 | Patch the current lockfile's reported affected ranges. Keep direct application dependency versions unchanged; include resolution-required companion updates. |

Validation: `aspect tidy`, full Bazel build, 99 passing Bazel tests with one
skipped, frozen Python sync with 17 passing tests, 59 passing locked Rust tests,
and a source-built Hauler version smoke test reporting Go 1.26.10. One local
control-plane integration test timed out while several builds ran concurrently;
it passed with tests serialized. No test timeout or assertion was relaxed.
The all-tool `//src/tools:pins` target cannot resolve macOS `psql` because its
unchanged lock entry has no macOS platform. Linux CI must complete that target.

Existing work remains separate and must be reviewed rather than duplicated:

- [#1568](https://github.com/guardian-intelligence/guardian/pull/1568) updates
  `x509roots/fallback` to `9f2b28d4331a`, whose upstream change removes roots
  associated with compromised intermediates. Alert relay imports this bundle.
- [#1588](https://github.com/guardian-intelligence/guardian/pull/1588) covers
  root-module x/net 0.60.0, gRPC 1.83.2 and compress 1.18.7.
- [#1617](https://github.com/guardian-intelligence/guardian/pull/1617) and
  [#1618](https://github.com/guardian-intelligence/guardian/pull/1618) cover the
  Vite and Vite Plus graph. #1618 also repairs Bazel tool bindings, refreshes
  affected transitive packages and regenerates the checked-in action bundle.
- [#1620](https://github.com/guardian-intelligence/guardian/pull/1620) repairs
  dependency-only image publication and prepares cloudflared 2026.10.3. Its
  rollout document records live versions, checksums, redundancy and recovery.

All work here is preparation. A reviewed main merge can publish and promote
images automatically, and a Postflight CLI lock change can start its separate
release lane. Obtain the operator's production rollout approval before merging.
Verify normal branch controls and exact-head CI first. Then track publication
at the merge revisions, immutable digests, image automation, canary health and
running versions; source updates alone do not establish deployed remediation.
