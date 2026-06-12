[AI GENERATED, CAUTION. NOT YET HUMAN APPROVED]

# aisucks.app — Charter

> "If AI is gonna say sh*t, it should at least be right about it."

Version 2 — 2026-06-12. This document is the constitution of the project. Designs,
SLOs, contracts, and code review all answer to it. It changes only by written,
versioned amendment in this repository.

Amendment record:
- **v2 (2026-06-12, operator):** value 2 amended — submission IP addresses are
  retained, quarantined, for abuse and threat monitoring and for compliance
  with valid legal process. Rationale: anonymity was leaving us defenseless
  against abuse and unable to answer lawful orders for chat message data
  without endangering the project. The promise copy was updated to the
  human-annotator wording at the same time. Legal posture (previously pending)
  is ratified as comply-with-valid-orders.
- v1 (2026-06-10): initial charter.

## Mission

Build the tightest feedback loop that exists between humans who catch AI being
wrong and the systems that train AI to be right — without ever knowing who those
humans are, and protecting what they share with us at all costs.

Anyone can report a model stating a falsehood by pasting a share link. We verify
the falsehood against the world, publish it with receipts, and compile it into
training-grade RL environments. The labs are held publicly accountable for their
errors and pay for the privilege of learning from them. Over time, anyone — not
just us — can author RL environments from verified human feedback, because
teaching AI about the world should not be a capability reserved for the
companies that build it.

## The wager

Continuous learning will not come from bigger pretraining runs. It will come from
tighter feedback loops between deployed models and the humans who notice their
failures. Today that loop is broken: errors are noticed millions of times a day
and recorded nowhere. The labs' own feedback channels are private, unaccountable,
and identity-bound. We bet that an anonymous, verified, public, adversarially
honest error stream is more valuable than a private polite one — and that
accountability pressure is what makes the labs buy it rather than ignore it.
This project is the first attempt at that loop. The wall of shame is the
incentive; the dataset is the product; the loop is the point.

## The flywheel

Revenue comes from labs buying access to the dataset — and from nowhere else.
That revenue funds more accountability: more verification capacity, more
providers covered, more errors confirmed. A more thorough record makes the
dataset more valuable, which generates more revenue, which funds more
accountability. The flywheel is the business model and the mission delivery
mechanism at once; they do not trade off against each other, they are the same
motion. Its one structural rule: labs are customers, never constituents. Their
money buys access to the record (value 4), never influence over it (value 1),
and never the people behind it (value 2).

## Values

Ordered. When two values conflict, the one listed first wins, and we then
engineer until the loser is satisfied too. "We couldn't hit the latency target
without a tracker" is a missing design, not a tradeoff.

### 1. Integrity of the record

The wall and the dataset are only worth anything if they are true. Nothing is
published without citation-backed verification. The verification configuration
(judges, prompts, thresholds) is public. Every published error is
cryptographically signed and appended to a tamper-evident log, so a lab can
prove we didn't fabricate an entry and we can prove they can't make one
disappear. Precision on the wall is an SLO with a freeze policy: if our
false-positive rate breaches threshold, publication stops until it's fixed.
No payment, partnership, or legal threat buys an edit to the record. A wrongly
published entry is corrected loudly, never silently.

### 2. Privacy is architecture, not policy

We do not promise not to look at personal data; we build so there is almost
none to look at. No accounts, no cookies, no fingerprinting, no analytics.
**One identifier is retained (amended v2): the submitting IP address**, kept
for abuse and threat monitoring and for compliance with valid legal process —
if we are subpoenaed for chat message data, the associated IP is part of what
we hold and may be compelled to produce. IPs live in a quarantined
abuse/compliance domain: its own store and keys, access-audited, bounded
retention (90 days unless under legal hold — period amendable), and exactly
two consumers — abuse defense and legal response. IPs are never analytics,
never product signal, never exported with any dataset, and never sold or
shared under any commercial terms. Outside that domain nothing changes: we
never correlate or attempt to draw links between submissions or submitters —
not for analytics, not for customers, not for us — and a deanonymization
capability beyond the abuse/compliance domain remains forbidden to build.
When a feature requires identity (see Escalation, below), it lives in a
quarantined consent domain with its own keys and a one-way reference; the
anonymous corpus never points back.

The data boundary has three tiers, hardest last:

- **Public** — wall entries: the false claim, the ground truth, citations,
  signature, and the upstream receipt link. A skeptic verifies an entry
  against the lab's own servers, not against our corpus.
- **Contracted** — the dataset labs buy: distilled, anonymized RL
  environments (value 4). Never raw conversations.
- **Sealed** — the raw corpus: transcripts as submitted. It is never sold at
  any price and never persisted anywhere but infrastructure we control; its
  only consumer is our verification and distillation pipeline. During
  verification, chat text may transit third-party models strictly under
  zero-data-retention, no-training terms — the sole exception, disclosed per
  The promise, and slated for elimination by self-hosted judges.

### 3. People own their data

A submitter owns their contribution; the humans inside a transcript own their
words. Concretely: revocation of an upstream share link, or a deletion request
for a transcript, is honored — including propagation into dataset records
already delivered to paying customers, and our contracts must say so. Identity
records in the escalation domain are deletable by their owner at any time,
completely. We store transcript text because the mission requires it; we hold
it as custodians, not owners, and our license to labs conveys access, never
ownership.

### 4. Open code, private data

The industry default is inverted here: they run secret code on your public
data; we run public code on your private data. Our code, infrastructure, and
manifests are open source, built reproducibly, with published digests — anyone
can verify the binary we run is the code we show. The data is completely
private: never published, never open-sourced, never tiered into the commons.

What a lab buys is never the data people shared. Raw chat logs and messages
do not leave — not to customers, not at any price. The product is
**distillation**: each confirmed error is compiled into an anonymized RL
environment — a synthesized minimal prompt that reproduces the failure, the
false claim, the verified ground truth, citations, and a grader — with the
original conversation's wording, structure, and incidental content left
behind. The submitter's words and the original user's words stay home; only
the error travels. Environments follow open standards where they exist, so a
lab consumes the stream with off-the-shelf tooling and anyone can author
compatible environments with the open tooling below.

Access exists only under binding contract, and the contract imposes our
values on the buyer — deletion propagation, no reidentification attempts, no
resale. A buyer unwilling to accept those terms does not get the data at any
price.

Democratization means the capability is open, not the corpus: the tooling to
verify claims and compile RL environments is open source, so anyone can run
this loop on their own feedback without surrendering it to us — or to anyone.

### 5. Hyper-performance, hyper-reliability

Speed and uptime are respect for the user and credibility for the mission — a
site about engineering quality that is slow or down is self-refuting. The page
is one bar, server-rendered, with no required JavaScript, targeting
time-to-interactive indistinguishable from time-to-first-byte. Availability,
latency, time-to-verdict, durability, and dataset-stream freshness all carry
SLOs with error budgets. High availability is achieved with infrastructure we
control; per value 2, we do not buy latency from third parties who see our
visitors. If the targets and that constraint conflict, the targets wait for
the engineering, not the other way around.

### 6. Engineering quality and release discipline

Boring, verifiable engineering: hermetic builds, pinned dependencies, idiomatic
code without frameworks, contracts before implementations. Nothing reaches
production without passing gamma: load at design-spike levels, migration
dry-runs, scraper canaries, and — for the verifier — a golden set of
known-true/known-false/known-induced claims with precision and recall gates.
The verifier promotes independently of the service. Rollback is a tested path,
not a hope. Everything runs unattended; humans appear only where the values
demand judgment (publication disputes, deletion requests, key ceremonies).

### 7. Authenticity without identity

We prove data is real without proving who brought it. The share link is the
mechanism: a live link to the lab's own servers is evidence the conversation
happened, requiring no trust in the submitter and no knowledge of them.
Manufactured errors (user instructs the model to lie) are detected by
provenance judging and excluded from the public record. Where we need more
anti-abuse signal than anonymity allows, we accept the abuse rather than the
identity.

### 8. Post-quantum cryptography where feasible

The corpus is stored for decades; "harvest now, decrypt later" is in our
threat model. Where the ecosystem supports it we use hybrid key exchange
(X25519 + ML-KEM) for transport, ML-KEM envelope encryption for backups and
stored secrets, and ML-DSA signatures for the dataset integrity log — so the
signatures proving our record honest outlive a cryptographically relevant
quantum computer. Where PQC is not yet feasible, the gap is documented, not
ignored.

## The promise (home-page copy, canonical and verbatim)

> Your chat and chat messages will never be sold to OpenAI, Anthropic, or anyone else. Expert human annotators convert a PII-redacted version of your shared link into an exam question for the next generation of AI. Learn more about how we protect your privacy and hold AI companies accountable.

This copy is a contract with every visitor, and each sentence binds the
architecture:

1. *"…never be sold to OpenAI, Anthropic, or anyone else"* — chat text is
   never part of any commercial transaction; the only thing ever sold is
   the distilled product (value 4). The word is "sold", not "shared",
   deliberately: it permits transient processing of chat text by
   third-party models during verification, strictly under zero-data-
   retention, no-training terms. That processing is a disclosed dependency
   on the "learn more" page — never a secret — and eliminating it by
   self-hosting the judges is the standing roadmap priority. The promise
   only ever gets stronger; this copy may be amended toward "shared" when
   the architecture earns it, never away.
2. *"Expert human annotators convert a PII-redacted version…"* — redaction
   precedes human eyes: annotators (ours or contracted, bound to these
   values by contract) work only on the redacted rendering of a transcript,
   never the raw text. Their output — the exam question — is the only path
   out of the sealed tier and carries none of the original wording.
3. *"Learn more…"* — a public page documenting the privacy architecture and
   the accountability mechanism exists at launch, in plain language, and is
   kept true.

The copy changes only by charter amendment.

## What we build

1. **The bar.** One page, one input. Paste a share link, get a verdict.
2. **The verifier.** Claim extraction → adjudication with citations →
   provenance check → signed, published record.
3. **The wall and the scoreboard.** Public, permanent (subject to value 3),
   per-lab, with receipts.
4. **The stream.** Cursor-resumable feed of distilled, anonymized RL
   environments — never raw conversations; contractual SLAs; access only
   under a value-binding contract per value 4.
5. **Escalation (opt-in).** A user may attach their identity to a report and
   have us escalate to the lab on their behalf — quarantined per value 2,
   deletable per value 3, never entering the dataset.
6. **The platform (horizon).** Tools for anyone to compile verified feedback
   into RL environments.

## What we will never do

- Track, fingerprint, or profile a visitor. (The sole retained identifier is
  the submission IP, quarantined in the abuse/compliance domain per value 2 —
  never used to profile, never analytics, never product.)
- Draw, or build the ability to draw, links between submissions or people
  outside the quarantined abuse/compliance domain (value 2).
- Sell, share, or disclose identity data — including the escalation domain —
  to anyone, including the labs, including under commercial pressure.
- Let a customer pay to alter, suppress, reorder, or pre-view the record.
- Silently edit or delete a published entry (corrections are public; honored
  deletions leave a tombstone stating that a deletion occurred).
- Publish an unverified claim as confirmed.
- Sell raw chat logs or messages to anyone, or provide them to labs in any
  form. The only product that ever leaves is distilled, anonymized
  environments under a value-binding contract; transient zero-retention
  processing during verification is the sole, disclosed exception to chat
  text touching a third party.
- Take revenue from anything other than dataset access — no ads, no
  sponsorships, no consulting for the labs we score.
- Add a second input field to the front page.

## Assumed values (explicit, pending founder ratification)

Ratified 2026-06-10: openness (public code, completely private data — value 4)
and revenue (dataset contracts only, per The flywheel). Still pending:

- **Precedence:** the ordering of values 1–8 above resolves conflicts.
- **Legal posture (ratified v2):** where protection meets a valid legal
  order, we comply rather than endanger the project. The v2 amendment to
  value 2 makes this concrete: submission IPs are retained and therefore
  compellable; the "learn more" page says so in plain language; and we
  publish a transparency report covering requests received and what was
  produced. Everything else about a submitter remains unstored and therefore
  uncompellable.

## Success looks like

- A skeptic can verify any wall entry end-to-end — claim, citations,
  signature, build digest — without trusting us.
- A lab cites the stream in a model card, renews the contract, and the
  flywheel visibly turns: contract revenue funds a measurable expansion of
  verification coverage the following quarter.
- A precision audit by a hostile party fails to find a fabricated or wrong
  entry.
- The deletion drill passes: a revoked conversation is provably gone from us
  and our customers.
- Someone we've never met ships an RL environment built on the platform.

## Amendment

Changes to this charter are commits: written rationale, version bump, history
preserved. The values' precedence order changes only with a stated reason that
future maintainers can judge us by.
