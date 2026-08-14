# Engineering rules

Rules that apply to every change in this repository, regardless of surface.

* Never download unpinned versions of software or set an unpinned version as a dependency. Binaries are versioned, built, packaged, and installed by Bazel declarations. This includes tools in src/tools.
* Invent nothing. If we write our own code, it should be glue code over existing libraries and apeing reference implementations of solutions to problems only. Prefer the boring industry-standard thing. Component choices are made by bake-off: candidates researched, losers rejected with recorded reasons, the winner pinned (the Hauler decision is the template). Months spent recreating an existing tool poorly is the cardinal failure mode.
* Avoid custom schemas, protocols, shell scripts, contracts.
* Zero customers as of present day besides us: no compatibility shims or legacy wrappers.
* For every feature we ship, we must assume that if we don't have a canary actively asserting it works, that it's broken. If the user suggests a feature or large project, work backwards from the monitoring and operations story: how can we be notified when the feature breaks, or when performance or availability drops, and how do we avoid shipping regressions in the first place using promotion gates and responsible deployment practices? We have the technology necessary to do so, we just have to remember to use them. Canary principles live in `docs/canaries.md`.
* Run load tests. Load tests are the best way to measure the durability and performance of our system. We must understand the maximum throughput of our system: individual components and blackbox user-session-simulations.
* The goal is to make operations run unattended, no human-in-the-loop.
* To safely configure secrets per-environment, read `docs/secrets.md`. Adding, rotating, or wiring a secret never opens the custody bundle: it is a Git PR plus one `bao kv put` through a namespace-scoped writer token. Custody is sealed for disaster recovery, cold boot, and CA/seal-key rotation, and `TestCustodyCeremonyConfinedToRecoveryRunbooks` fails the build if any other document teaches the restore ceremony. The bootstrap OpenTofu roots reconcile in-cluster (`docs/tofu-gitops-design.md`), so a routine root change is a PR, not a custody open; the workstation `tofu apply` survives only as break-glass.
* Do not use GitHub Actions workflow YAMLs as a second control plane. Prefer to move tasks including but not limited to: generating Preview Deployments, generating/signing images, scheduled jobs, and so on, into the source code, rather than hairpinning cluster administration through GitHub.
* You are not alone in this repo. Expect parallel changes by the user or other agents and work around them to avoid destructive action.
* No need to be precious with git hygiene. If you see a doc update, it's fine to fold it into your worktree or branch, even if it's unrelated.

## Service and API conventions

* API IDL in Buf/Connect + (AIP-193). Declare each operation's policy surface (e.g. required permission, idempotency key, request-size, rate-limit class, audit level) outside of the core event contract as method-options metadata on the RPC contract. We need to be able to fine tune operational characterstics that don't break the schema. See `src/proto/guardian`. `connect.Interceptor`s enforce it fails-closed.
* Protobuf governance uses the repo-pinned Buf toolchain through Bazel: linting, formatting, and breaking-change checks run from `rules_buf`; code generation uses local pinned generators only. Do not use Buf remote plugins in build/test/release paths.
* Use SQLC+pgx for Go service PG queries.
