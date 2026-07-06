# Verself — Product Document v0.6

2026-07-05. Source document for the sales training deck. Market and buyer claims are research-verified (July 2026); technical claims are verified against Verself source or measured on our metal. Sections and lines marked ⚙ are internal — they inform the deck but never appear in customer material.

---

## 1. The product

**We provide CI agentic engineers love.**

Differentiation, two claims only:

1. **We are faster than everyone else.** Bare metal, warm capacity, and source + caches present as local NVMe block devices before the VM boots. Every other claim in this document is evidence for this one.
2. **Agents are first-class users.** Debugging CI is a prompt to an agent. Your engineers never learn a CLI — they type `/verself` and Claude or Codex takes care of the rest.

⚙ Each claim carries an obligation. "Faster than everyone else" is benchmarkable against vendors who publish scoreboards (RunsOn's tables are the industry's neutral eval; Namespace currently tops single-thread) — we win that benchmark and publish in their format, or we soften the claim. The hardware selection (§14.1) gates the headline, not just the rate card. "Agents are first-class users" must be shipped capability (§6), not copy.

## 2. The story (deck opening — hot cognition)

> The bottleneck isn't your engineers. It's the feedback loop.
>
> A 20-minute CI run means your best engineer context-switched twice before finding out a test failed. Nobody great reads CI logs by hand anymore — their agents do, or they leave for a company where that's true.
>
> Fast CI stopped being an infrastructure line item. It's a retention line item — and it's what your engineers tell their friends about.

Evidence behind the story, for the proof slides:

- CI volume is growing ~60% *per quarter, per developer* (Blacksmith's measured figure) as agents multiply PR volume ahead of headcount. The feedback loop is compounding in the wrong direction on stock runners.
- Real champions say it in their own words: "developers became incredibly vocal that the pipeline wasn't great" (Jane, 250 devs); "ran the pipeline 10 times to get one pass" (Clerk); "The amount we pay an engineer per minute is far higher than any CI minute" (Upbound, Director of Eng).
- Pitch order: morale story → founder credibility → live demo ("your agent installs it before this meeting ends") → the benchmark → the meter and the commitment. Emotion, trust, proof, demo, deal. Infrastructure never leads.

## 3. The founder (why buy from me)

Early customers aren't buying a runner; they're buying the person operating it. The bio is the warranty, and it maps onto this product with unusual precision:

- **Shovon Hasan — capacity products are the day job.** At AWS, leads development of the consoles and developer tooling for EC2's capacity products — **Capacity Reservations, Spot, and Capacity Blocks for ML** — and builds the developer tooling used across ~25 engineers' surfaces including Bedrock inference (Mantle), Console to Code, Launch Wizard, and Fleet Manager. Deck translation: reserved capacity, spot markets, and metered ML capacity are what he builds at planetary scale; Verself is the same product category, operated at boutique scale with one name on the pager.
- **Agent-native practitioner, not tourist.** Daily agent-driven development since Cline in May 2025 — roughly twelve hours a day since. The product's user is an agent because the founder has spent over a year living that way; the skill and CLI are designed from use, not from spec. ⚙ Verbal-only color, never in writing: the reason moonlighting works is that he's automated his own day job (and much of his team's) with agents — the product's thesis, demonstrated on its founder. Great bar story, contractual/optics hazard in a deck.
- **The FDE commitment, in writing:** Shovon personally forward-deploys into your org. Any bug, outage, latency regression, or janky feature is prioritized immediately — not triaged into a backlog. The headline for the common case: a fix ships within 24 hours. Founder-paged support isn't a support tier — it's an engineer on your team you don't have to hire.

⚙ The written form of the commitment is a standard severity-tiered support agreement, so the promise survives physics (the design-partner one-pager, ~half a page):
  - **Sev-1 (CI down / security):** acknowledged within 1 hour; mitigation effort is continuous until restored; hourly status updates.
  - **Sev-2 (degraded performance, blocking bug):** acknowledged within 4 hours; workaround or mitigation within 24 hours.
  - **Sev-3 (minor bug, janky feature):** prioritized ASAP; a committed timeline communicated within 24 hours.
  Response and mitigation are on the clock; root-cause resolution is committed as continuous effort plus a communicated timeline — the industry-standard shape, generous at the tier levels because there are ten customers and one very motivated engineer.

⚙ AWS status — resolved: Shovon is at AWS until outside investment (or revenue) makes Verself the full-time job, and the material says so honestly rather than hiding it. Handle it *proactively* in every deal (see §9's talk track) — raised by us before they ask, paired with why the platform doesn't depend on founder-hours: the substrate is built for unattended operation and pages a human only when self-healing fails.

## 4. Who buys (evidence-based ICP)

Research across ~30 competitor case studies gives two real profiles and one conquest note. The dividing line is published by the incumbents themselves: **~75 engineers / ~$50M raised** is where self-serve breaks and commitments begin (Blacksmith: "around 75 engineers in, costs spike, flakiness grows, and scaling pains kick in").

### ICP-A — the compile-bound AI-era product team (land)

- **Firmographics:** 15–75 engineers, seed–Series C. Devtools, AI, fintech-infra. Compiled or Docker-heavy stacks over-index hard (Rust appears in ~half the strongest case studies; monorepos and SDK matrices qualify). Private repos, Linux-dominant CI.
- **Champion:** below ~30 engineers, a founder/CTO; 30–75, a named IC — staff engineer, DevOps/SRE, platform lead — *the person who personally waits on CI*. Measured on ship velocity and their own wait time. Fears owning CI forever and a migration that eats a quarter (every competitor testimonial praises "one line of YAML" for this reason).
- **Economic buyer:** the same person or one hop up. Card-on-file territory.
- **Triggers, ranked by evidence:** (1) PR-check time crossing ~10 minutes / queues visible in standup; (2) bill shock or the 10GB cache wall ("Ten gigabytes in today's world is peanuts" — Bastion CTO); (3) agent adoption multiplying CI jobs ahead of headcount.
- **Disqualifiers:** <5 engineers or hobbyists; Hetzner-DIY cost culture; pure price shoppers (Ubicloud owns them at $0.001/min; GitHub's January cuts compressed that segment — BuildJet died of it).

### ICP-B — the scaled company hitting the 75-engineer wall (commit — the revenue)

- **Firmographics:** 75–500 engineers, Series C → public. Often consolidating multiple CI systems; frequently a monorepo. Where Jane (250 devs, 35 teams), PostHog, PlanetScale, and the Namespace unicorn cluster (Ramp, Vanta, Verkada) live.
- **Champion:** Staff DevOps/Platform Engineer or Director of Platform Engineering — measured on pipeline success rate, CI spend, and developer-survey sentiment. Stated fear, verbatim from the field: championing a small vendor and eating an outage ("both of these providers are essentially single person operations" is now a written objection). Vendor continuity is a buying criterion after two 2026 shutdowns — §9's bus-factor talk track is the counter.
- **Economic buyer:** VP Eng/CTO signs; security gates. The enterprise tiers every incumbent gates (SSO, audit logs, SLA, invoice billing) are the artifacts of this procurement pass.
- **Triggers, ranked:** (1) **self-hosted ARC/runner-fleet burnout** — the single best-evidenced negotiated-deal trigger ("sleepless nights keeping it running" — Finch); (2) **a trust rupture with an incumbent** — PlanetScale left Buildkite over a downtime demand; switching happens on trust, not price; (3) **agent-volume shock** hitting concurrency ceilings and budget predictability (Astral hit GitHub's org concurrency caps with a ~900-job matrix; Nominal's trigger was AI-tool PR growth).
- **Disqualifiers:** teams whose real requirement is secrets-in-house orchestration at monorepo scale — they're leaving the GitHub Actions control plane entirely (Buildkite/RWX buyers; PagerDuty ran a 50-vendor eval to get there). That's our BYOC rung later (§13), not our hosted product today.

### Conquest notes (time-boxed)

- **Cirrus Runners run-off:** contracts expire through 2026 with no first-party migration path. The macOS-primary mobile fleet is *not ours* (we don't do macOS — refer them out, honestly). The **Linux-slot** portion bought fixed monthly capacity for budget predictability — exactly our commitment shape.
- **Blacksmith billing-surprise refugees** (June 2026 HN scandal): approachable with the spend-cap + commitment story.

⚙ **Design-partner selection rule:** the first 10 come from ICP-A *behavior* (founder-reachable, one-hop approval, 5-minute install) but are selected for ICP-B *trajectory* — 30+ engineers, agent adoption visible, approaching the wall. The research's sharpest insight: **the self-serve champion and the committed buyer are the same person at two moments in company life.** The IC who felt the queue at 40 engineers runs the procurement pass at 150. Design partners are champion-farming; the $40k/mo customer is ICP-B, reached through references, and reference calls are how that segment actually buys (the biggest enterprise case studies in this market are deliberately anonymous).

Where these people are: HN incident/pricing threads (the proven channel — every founder in this market sells in comments), RunsOn's public benchmarks used as neutral eval material, the GitHub pricing-revolt discussion threads, OSS maintainer blogs, and in person in SF.

## 5. Pricing & packaging

**The meter is always on: per-millisecond, for everyone.** The ledger meters guest workload time (not VM boot overhead) in milliseconds. Nobody else can say this — Blacksmith bills per-minute (a 10-second agent-triggered job bills as a full minute; users complain about exactly this), Depot per-second. Millisecond metering is the *fairness* story for the agent era: thousands of short agent-triggered jobs, billed for what they use.

**What varies is the deal shape on top of the meter:**

- **Metered (ICP-A, design partners):** usage-billed against the meter with **hard default spend caps** and real-time usage from the CLI. Raising a cap or adding reserved capacity requires explicit human approval — agent prepares, human ratifies. Billing surprises are a product defect, not a revenue line.
- **Committed (ICP-B, negotiated bespoke):** committed spend in exchange for negotiated rates, **guaranteed reserved warm concurrency**, priority capacity, SLA, dedicated Slack, invoice billing. Settled against the same millisecond meter — a commitment is a floor and a guarantee, not a different billing system. Every enterprise deal is negotiated individually; there is no public enterprise rate card.
- **Hardware isolation scales with the commitment.** At sufficient committed spend, we provision nodes that are exclusively yours — physically dedicated bare metal, operated by us. On our economics this is a strength, not an exception: capacity is only ever added when a concrete commitment pulls it, so a large deal literally comes with its own machines. The isolation ladder a buyer climbs: isolated VMs on shared metal (everyone) → reserved warm concurrency (committed) → dedicated nodes (larger commitment) → BYOC on their own metal (§13, later). Each rung is the same product with a stronger boundary.
- **Included in every deal, not add-ons:** dedicated static egress IPs (a $100/IP/mo add-on at Blacksmith; for us also the shared-IP rate-limit fix, §12), the durable cache system, docker mirror, and the founder FDE commitment (§3).
- **Design partners (first 10):** months free, founder paged on a Slack message, in exchange for written feedback and reference rights. **Payment instrument on file even when free** — free CI compute is the top cryptomining-abuse target; non-negotiable. 3-month pilot term; annual discussed after conversion.

⚙ Rate card is gated on the hardware benchmark (§14.1). Cost base is founder salary + compute + minimal admin; the metered rate must clear node cost at realistic utilization, and committed deals price the reserved RAM/cores they pin. Positioning rule: we never compete on price per minute — we sell speed, guaranteed concurrency, isolation, and predictability. Where warm capacity comes from (standing pools today, smarter lifecycle later) is an implementation detail and never appears in customer material.

## 6. Product surface: the skill is the interface

Surfaces, in the order customers meet them:

1. **The GitHub App** — where the daily loop lives: PR comments and checks. Minimal permissions are the security headline: **Actions (read) + org Self-hosted runners (read/write) + Metadata (read)**. We cannot request access to secrets or code — GitHub doesn't offer it.
2. **The skill — a major part of the product, not documentation.** Humans never learn the CLI; they type `/verself` (or ask their agent) and Claude or Codex does the rest: onboard, integrate a repo, diagnose a failure, pull usage, manage the org. Design principles:
   - **User-invokable only by default, zero ambient context cost.** The skill occupies no tokens until called. An agent-native product must not tax the agent's context window to exist — this is a design requirement, not an accident.
   - The skill encodes the golden paths and teaches the agent to self-serve diagnosis; it is versioned, shipped, and supported like the CLI itself (the §3 FDE commitment covers skill bugs explicitly).
   - Distribution: official marketplaces (Claude Code plugins, Codex plugins, Cursor rules — thin adapters generated from one source) plus `curl verself.sh/agents | sh`. The marketing page serves AGENTS.md to agent traffic.
3. **The CLI — the substrate the skill drives.** Engineers can use it directly; agents always can: `--json` everywhere (JSON by default when stdout isn't a TTY), non-interactive by default, distinct exit codes, structured errors with remediation text, idempotent commands, a `verself api` escape hatch.

**"Debugging CI is a prompt" — the shipped capability behind claim #2:**

- `verself jobs describe --json` returns agent-actionable failure context: failing step, exit code, log tail, diff-vs-last-green-on-main, cache-state deltas.
- The GitHub App's failure comment carries the same context in agent-readable form — the agent in the customer's PR loop diagnoses without being asked.
- The skill teaches the agent to pull context and propose the fix. Incumbents built dashboards for humans staring at logs; that's the behavior our story says is dying.

**A verifiable install path — trust designed for the agent era.** The CLI and skill are cosign-signed with SLSA provenance and inspectable attestations. When an org tells its agent to install a new vendor's tooling, the agent verifies provenance *before executing anything*, and the security reviewer can audit that it happened. No CI incumbent offers an attestable install path today. ⚙ The machinery exists (keyless CI identities, offline-verifiable bundles); the skill's install step runs the verification by default — whether agents "appreciate" it is unfalsifiable, but security teams reviewing agent-driven installs measurably do, and it converts the scariest part of our pitch (new vendor + agent + binary) into a differentiator.

**Closing the console-shaped gaps headlessly** (each is a known failure mode of console-less products):

- Finance: Stripe-hosted Customer Portal is the billing console (invoices, receipts, payment methods; email+OTP login for the CFO). Committed deals get invoices.
- Security: audit-log export/streaming as a first-class CLI feature (SIEM export is what larger customers prefer anyway) + public trust page, which publishes the data-retention policy: everything we hold is encrypted, expires in 30 days, and is rebuildable from the customer's repo (§12).
- Spend: hard default caps + real-time `verself usage` + human approval on anything that raises the bill.

Auth: PKCE loopback is the default `verself login` (agent does everything; human clicks approve once); device flow behind `--headless` only (device-code phishing spiked 37× in 2026; security teams block the grant — AWS CLI made the same default switch). `auth.md`/ID-JAG platform-attested agent registration when the standard stabilizes.

⚙ No-console is a sequencing bet: instrument every "I needed a UI for X" ticket; the escape hatch (read-only status/usage page) is a feature release, not a strategy reversal.

## 7. Onboarding: the one-prompt integration

The line: *"Take 5 minutes. Install our GitHub App, tell your agent to finish the job, and watch your next PR."*

1. Human installs the GitHub App (or the skill first — either entry works).
2. Agent invokes `/verself` → runs `verself login` — PKCE loopback, one human approve click. The skill's install step verifies cosign/SLSA provenance before anything executes.
3. Agent runs `verself init` — detects repos and opens migration PRs **with the customer's own credentials**: swaps `runs-on`, swaps `actions/checkout` → `verself/checkout`, authors `.verself/cache.yml` from repo analysis (bazel/pnpm/cargo/gradle paths — judgment work a customer's agent does well).
4. First merged PR → next job lands warm → the GitHub App comments before/after timing on the PR. The product demos itself in the customer's own repo.

⚙ Step 3 is structural advantage twice: competitors' migration wizards need code/PR write on *their* App (the permission security reviewers reject) — ours needs nothing because the customer's agent does it; and wizards only do mechanical `runs-on` swaps — an agent authors the cache manifest that unlocks the real speedup. Honest demo nuance: a bare `runs-on` swap gets faster CPU but leaves `actions/cache` on GitHub's backend (~20 MB/s from external DCs — worse than GH-hosted). The full win needs all three changes, which is why the agent does them in one PR. Stock actions still work as a compatibility fallback; they're not the product.

## 8. Performance claims (sales language guardrails)

- ✅ "Your job starts in ~5–15 seconds of the webhook — the residual is GitHub's own assignment latency, which no vendor can compress. Hosted runners queue 10–60s+ with multi-minute tails."
- ✅ "Zero queue up to your reserved concurrency. P99 start time = P50. Nobody can sell you that from a shared pool."
- ✅ "Substantially faster single-thread CPU than GitHub's runners, with source and caches already mounted as local NVMe block devices at boot." (Exact multiplier comes from our published benchmark — §14.1 — never improvise it.)
- ✅ "Green on main checkpoints your whole warm state — source, artifacts, declared caches. Your next run starts from it."
- ❌ Never "90ms starts" for CI — that number belongs to the direct sandbox API (later product; no GitHub handshake in that path). Measured on our worker metal: ~520ms full warm restore, ~10 VMs/s under parallel restore.
- ❌ Never "we insulate you from GitHub outages" — webhooks and job assignment are GitHub's control plane; every vendor rides it. Customers know this; overclaiming here burns trust (it burned Blacksmith).

## 9. Objection playbook

**"We already use Blacksmith."**
"Blacksmith is good — we benchmarked them. Three structural differences: (1) *Metering* — they bill per-minute; your 10-second agent-triggered jobs bill as full minutes, and their metered free tier accrued a customer a surprise $1,081 invoice in June. We meter by the millisecond, with hard spend caps you set. (2) *Capacity* — they have no dedicated or reserved option at any price; you share their pool and their outages (this month: cache outage plus job-history data loss). We reserve warm concurrency for you contractually. (3) *Support* — you Slack me, I get paged, and if your agents hit a CLI or skill bug I ship the fix within 24 hours. And static IPs are included, not $100 a month."

**"GitHub Actions is fine."**
"If CI isn't a felt pain, we're not a fit. But check three numbers: you're paying $0.006/min for roughly half the CPU we run; your cache caps at 10GB with 72-second round-trips; and 'stuck in Queued' has its own community megathreads. Ask your engineers — they know."

**"We already use Depot."** *(will come up — they're the strongest incumbent)*
"Depot's real strength is Docker builds — keep them for that if you love it. Where we win: metering (ms vs seconds), dedicated reserved capacity (their dedicated infra is a sales-gated enterprise checkbox), a cache that's a block device mounted before boot rather than a faster download, and an agent-debuggable surface. And you're a name to me, not a tier."

**"You're one person."**
"One person whose day job is EC2's capacity products — Capacity Reservations, Spot, Capacity Blocks for ML. Reserved compute with a meter is what I build at planetary scale; this is the boutique version with my name on the pager. Your CI runs on capacity I don't oversubscribe, the substrate is open source with drilled disaster recovery — cold-boot from nothing is a rehearsed procedure — and leaving us is a one-line `runs-on` flip back to `ubuntu-latest`; I'll hand you that runbook today. Plus the support commitment in writing: anything broken gets prioritized immediately, most fixes ship within 24 hours." ⚙ Hand the runbook over proactively; after two vendor shutdowns this year, continuity is a stated buying criterion and the runbook converts fear into trust.

**"Isn't this a side project? You work at AWS."** *(raise it ourselves before they do)*
"Yes — I'm at AWS until this business earns my full attention, and I'd rather you hear that from me. What it means in practice: the platform is built to run unattended — it detects its own degradation, heals what it can, and pages me only when it can't; my support commitment is in writing with severity tiers; and your exit is a one-line runbook I hand you on day one. And candidly: design partners like you are exactly how this becomes my full-time job." ⚙ Proactive honesty here is a trust weapon — a hidden day job discovered later kills the deal; a disclosed one with a written SLA and an exit runbook reads as integrity. Calibrate warmth; never the belligerent version.

**"You want our agents installing a new vendor's binary?"**
"That's the one install in your org your agent can *cryptographically verify first*. CLI and skill are cosign-signed with SLSA provenance; the skill's install step verifies attestations before executing, and your security team can audit that it happened. Compare that to the marketplace actions your CI already runs unverified."

**"Are you SOC 2?"**
"In flight — Type II window opens [date]. Until then: trust page, isolation architecture doc, pentest letter, and I'll walk your security team through the design live. The architecture answers what SOC 2 can't: one job per VM, VM destroyed after, workloads isolated per customer with per-org encrypted storage, everything we retain expires in 30 days and is rebuildable from your repo, and a GitHub App that cannot request secrets or code access."

**"What about our secrets on your metal?"**
Never dodge: "GitHub injects secrets into the job runtime on every vendor — including GitHub's own runners. On ours: one-shot VM destroyed with all state, fully isolated from every other customer's workloads, per-org encrypted datasets at rest with 30-day ephemeral retention — nothing we hold is data of record — and our App can't read secrets or code; GitHub doesn't offer us that permission. Dedicated hardware is available on committed deals if your policy requires it. Here's the isolation doc." ⚙ CircleCI 2023 is what they're thinking of; answer before they name it. Never claim dedicated hardware as the default — isolation is the universal truth, dedicated is the committed-tier option.

**"What if you're down?"**
"Our watchdog pages me from queued-job aging before you notice; the status page says so; the runbook flips you back to `ubuntu-latest` in one line until we're green. GitHub holds a queued job 24 hours — you lose time, never a job."

**"Do you do macOS / ARM / Windows?"**
"No, and I won't pretend otherwise. Linux x64, done extremely well. For macOS: [named referral]. Your Linux jobs are most of your minutes; split the matrix."

**"We're moving to Buildkite / we need secrets in-house."**
"Then you've outgrown the GitHub Actions control plane and you should go — that's a different product. Talk to me again when you want this performance on your own metal." ⚙ Qualify out honestly; this prospect is the BYOC rung's pipeline (§13), and honest disqualification is what makes the reference network work.

## 10. Non-goals (v0)

- macOS, Windows, ARM.
- Public repos / fork PRs. Private repos only — deletes the fork-PR threat model (`pull_request_target` cache poisoning, approval bypasses) rather than mitigating it. The push-to-main golden-promotion gate is already the right trust shape for revisiting later.
- Burst beyond reserved concurrency for committed customers; metered customers queue with watchdog alerting (sustained 80% = the more-capacity conversation, made from data).
- Web console.
- Self-serve signup without a conversation. Ten hand-picked customers is the strategy.
- Competing on price per minute.
- actions/cache protocol interception (Twirp shim) — deferred; built only if design partners refuse the checkout swap.

## 11. Compliance path

WarpBuild's playbook: straight to SOC 2 Type II — 3-month window, ~$8–10k, ~7 engineer-days on Sprinto/Oneleet-class tooling. Until it lands: trust page, published isolation architecture, pentest letter, subprocessor list, "controls in place since launch" framing. Design partners tolerate pre-SOC2; their customers' questionnaires flow downhill within ~2 quarters, so the clock starts at first paying customer.

## 12. Technical foundation ⚙

**Warm pool on QEMU — measured on guardian-w1 NVMe 2026-07-05:** ~520ms full warm restore vs 8.8s cold; 8-parallel restores in 774ms wall (~10 VMs/s); disk hot-attach 227ms with revoke verified. Assignment = resume + clock resync (kvmclock + guest-agent time set) + identity + JIT config fetched in-guest (attempt-scoped token). One-shot VMs, always. Workers: plain Ubuntu 24.04, no Kubernetes — every resource is customer compute; host daemon as a systemd unit dialing out to the control plane (egress-only, workload identity). Shared-nothing nodes; org→node affinity keeps caches warm; placement is a control-plane decision.

**Cache model — the moat (Verself-source-verified semantics on the new QEMU mechanism):**

- Source and caches are **local NVMe block devices, never a download protocol** — the customer-facing claim and the structural answer to every competitor's cache benchmark. **The core mechanism is hot-plugging zvols into running QEMU VMs** — tracer-proven 2026-07-05: 227ms attach, revoke verified, virtio-scsi controller must be in the VM template. Hot-plug is what turns a generic warm pooled VM into *your* VM at assignment time (the org isn't known at pool-boot), and is the expected path for non-enterprise customers; staging drives before boot remains available for golden-restore and dedicated-capacity paths. ⚙ The old "mount before boot, never hot-plug" invariant was the Firecracker-era design — do not carry it forward as a constraint.
- `verself/checkout`: guest fetches a single-commit git pack from the host's bare mirror, advances the durable workspace to the exact SHA, preserves untracked build state. Laptop-style incremental builds across CI runs.
- `.verself/cache.yml`: named cache mounts as ZFS zvol generations, CAS pointer promotion, scoped by branch + job-shape + trust class, 7-day TTL + watermark eviction.
- Green on push-to-main → golden checkpoint: vmstate+memory snapshot atomically coupled to the zvol generation set (including `_work`), CAS-promoted. PR runs consume goldens; only protected-branch pushes create them.

**Data lifecycle & isolation policy (canonical — agents keep getting this wrong; this is the reference):**

- **Golden images and durable caches: encrypted always, 30-day retention, NOT backed up by default.** They are *intentionally ephemeral* — rebuildable performance state derived from the customer's repo, never data of record. Losing one costs a cold build, never data. Backup of golden state is opt-in, not default.
- **Customer workloads are isolated from each other in the hosted offering**: one-shot VM per job, per-org encrypted datasets, no guest-to-guest reachability, no cross-tenant state sharing. This — not "dedicated hardware" — is the universal claim. **Dedicated hardware is absolutely on the table, sized to the commitment** (a large enough deal gets its own nodes — capacity is added when a commitment pulls it); it is negotiated, never an implied default.
- Sales upside of the policy, use it: "everything we hold of yours is encrypted, expires in 30 days, and is rebuildable from your repo — we are structurally a bad place to steal data from."

**Day-one table stakes** (each breaks the product in customer-blames-us ways):

1. **Docker pull-through mirror, on by default** — for customers' public-image pulls (`postgres:16`, testcontainers). GitHub-hosted runners are exempt from Docker Hub's 100 pulls/6h/IP limit; self-hosted are not. Implementation: proxy-cache registry (Harbor proxy project or equivalent) reachable from worker guest NAT; our own artifacts stay on ghcr + dark bundle, unrelated.
2. **Runner-binary lifecycle automation** — 30-day update rule silently stops job delivery; v2.329.0 registration floor enforced **Sep 25, 2026** (inside launch window). Inject the runner binary at pool-fill from a local artifact store; watch actions/runner releases; empirically test whether JIT configs disable auto-update.
3. **Queued-job watchdog + status page** — no fallback-to-hosted exists; a starved job sits silently up to 24h. Watchdog ages queued-without-in_progress and pages us first. Don't reap idle VMs aggressively (GitHub re-queues at 60s).
4. **The image is the compatibility promise** — runner-images clone: user `runner` UID 1001, populated `/opt/hostedtoolcache`, NOPASSWD sudo, docker group, swap, `/dev/kvm` (nested virt; Android emulation is silently 5–10× slower without it), dual-stack IPv4-default, ~30GB+ disk, UTC, en_US.UTF-8.
5. **Per-customer static egress IPs** — the rate-limit/abuse fix (Maven Central blocks shared hosted-CI IPs for 24h+) that is simultaneously a competitor's $100/mo add-on.

**Scale ceilings to instrument now, worry about later:** org runner-registration 1,500/5min (~5 job-starts/sec/org); installation-token quotas (5,000/hr base; ≤900 points/min/endpoint). Track rate-limit headers from day one.

**GitHub App manifest:** Actions (repo): read + Self-hosted runners (org): read&write + Metadata: read + `workflow_job` webhook. Org-scope registration always. Runner group with "Allow public repositories" OFF (default; structural safety net).

## 13. The product ladder ⚙

1. **Now — CI:** metric is webhook→start seconds, job duration, zero-queue.
2. **Next — direct sandbox API for agents:** same pool, no GitHub handshake; where <100ms spawn lives and Daytona is the competitor. Per-ms metering and snapshot/fork primitives already fit. No CI decision may foreclose this (lease API stays product-agnostic).
3. **After POC — BYOC:** license the control plane + scheduling onto customer compute; the installable artifact is the dogfooded worker bootstrap, dial-out egress-only. Competitive set becomes Buildkite hybrid / RunsOn / Actuated. §9's qualified-out Buildkite prospects are this rung's pipeline.

The market is converging on this exact ladder (Depot CI → agent sandboxes; OpenAI × Cirrus). CI revenue funds the pool; the pool is the sandbox product; the installer is the BYOC product.

## 14. Open decisions

1. **Hardware + rate card (gates the "faster than everyone else" headline).** f4.metal.small (EPYC 4484PX — desktop-class single-thread, competitive with premium vendors) until $4k MRR; rs4.metal.xlarge (9554P, 64c/1.5TB) adds density but its 3.1GHz base likely *regresses* single-thread — possibly right for sandbox density, wrong for CI slots. Benchmark rs4.metal.xlarge and f4.metal.large (9275F-class, 4.1GHz) one hour each: single-thread + a real CI suite (`bare-metal-ci-bench` is prior art). Publish the winner in RunsOn-comparable format.
2. **Design-partner terms:** months free (how many?), written feedback/reference ask, card-on-file (recommend: always).
3. **SOC 2 start:** now vs first paying customer (recommend: first paying customer; trust page now).
4. **Runner label namespace** (`runs-on: verself-16vcpu`?) — lives in customer workflow files forever; decide before first install.
5. **macOS referral partner** — one named vendor for the objection script.
6. **Cirrus Linux run-off + Blacksmith-refugee outreach** — direct outreach, or SF-in-person only for the first 10?
7. **Metered rate + spend-cap defaults** — after the benchmark; caps default low and raise on request.
8. **Design-partner support one-pager** — draft the severity-tiered commitment from §3 into signable form.

Resolved: 3-month pilot term · name stays Verself for now (label namespace + GitHub App name are the two customer-visible rename surfaces) · millisecond metering universal, commitments negotiated bespoke · QEMU warm pool, workers on plain Ubuntu · skill is user-invokable-only with zero ambient context cost · install path is cosign/SLSA-verifiable by the installing agent · AWS stays in the bio, disclosed proactively, until outside investment (§3, §9).

## 15. Known risks ⚙

- GitHub's postponed $0.002/min self-hosted fee returns → re-prices the category overnight (still leaves us cheaper; committed customers are insulated by contract).
- GitHub's ~5–15s assignment latency is our floor; their regressions look like ours (watchdog data is the defense).
- Zero-console has no surviving precedent; instrument for the escape hatch.
- Small fleet: one customer's growth can collide with another's — committed concurrency terms are the protection; metered customers get honest queue-alerting.
- Checkout-swap onboarding is more migration than a pure `runs-on` swap — mitigated by the agent doing it; if design partners balk, the actions/cache shim moves up the roadmap.
- ICP-C (Cirrus run-off) is time-boxed to 2026 — the base re-homes with or without us.
- "Faster than everyone else" is falsifiable by a public benchmark we don't control — win it before we say it.
- The FDE commitment is a contract term once written — the severity-tiered form (§3) is what makes it survivable: response and mitigation on the clock, resolution as continuous effort + communicated timeline. Must still survive founder vacation/illness; the day-job disclosure (§9) and this SLA must never be in tension in the same document a lawyer reads.
