# Canaries

How we build and run synthetic user journeys. These are principles, not law —
each carries its reasoning so future work can judge the exceptions.

1. **Canaries slower than 60 seconds don't gate frontend deploys.** Frontends
   should deploy fast. A slow journey — real browser, third-party login —
   belongs in the steady-state ring watching prod continuously; deploy gates
   are for cheap checks and real-traffic funnel metrics.

2. **Make synthetic traffic easy to filter, but don't bend over backwards.**
   Testing the product matters more than pristine analytics. Keep the
   correlations — account ids, IPs, user agents — detailed enough that canary
   traffic can be scrubbed from business dashboards systematically.

3. **Exercise third-party dependencies, don't mock them.** On critical paths
   that run through GitHub or another provider, we need to know when customers
   are having a bad time even when it isn't technically our fault. "Don't test
   your dependencies" is advice for teams with someone else watching the
   vendor; we are a single team.

4. **A canary that can't run is a finding, not a skip.** Broken credentials, a
   denied cleanup call, a consent page that changed shape — surface these as
   loudly as a failed journey. Canary programs die silently, not dramatically.

5. **Make cleanup safe by shape, not carefulness.** Whatever tears down
   synthetic state should be structurally unable to touch real users — a
   scoped grant, a group boundary — rather than relying on a correct allowlist
   in code. Then the cleanup code can stay simple.

6. **Treat canaries as handling critical data unless a documented exception
   says otherwise.** Canary credentials sit adjacent to management
   infrastructure. Keep captures off by default (traces, video, HAR), redact
   by known value at the source, and sanitize exports at pipeline choke points
   with industry-standard tooling.

Journey code lives in `src/products/viteplus-monorepo/packages/canary-journeys/`.
The Sign in with Guardian canary contract is in
[sign-in-with-guardian.md](sign-in-with-guardian.md).
