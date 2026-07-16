// Postflight security-architecture docs (/postflight/docs). Published ahead
// of the build on purpose: the confidentiality claim only works if customers
// can check it, so every mechanism carries an implementation-status label
// instead of the page waiting until everything is true.

export type MechanismStatus = "live" | "partial" | "design";

export const STATUS_LABEL: Record<MechanismStatus, string> = {
  live: "Live",
  partial: "Partial",
  design: "Design",
};

export interface LifecycleState {
  readonly id: string;
  readonly name: string;
  readonly status: MechanismStatus;
  readonly summary: string;
  readonly hostSees: string;
  readonly onFailure?: string;
}

export const POSTFLIGHT_DOCS_META = {
  title: "Postflight security architecture — Guardian Intelligence",
  description:
    "The lifecycle of a Postflight CI job on confidential hardware: what the host can see at every state, where the keys come from, and what is built today.",
} as const;

// The guest lifecycle as a state machine. Order is the happy path; onFailure
// captures the branch. "hostSees" is the complete view the Guardian-operated
// host has of customer data while the state holds.
export const LIFECYCLE: readonly LifecycleState[] = [
  {
    id: "S0",
    name: "Launch",
    status: "partial",
    summary:
      "The host asks AMD's on-die security processor (the PSP) to boot a VM from Guardian's published golden image. The PSP cryptographically measures every byte before any of it runs, and an ID block binds the expected measurement and policy so the PSP refuses to launch anything else.",
    hostSees: "An opaque VM it cannot introspect.",
    onFailure:
      "A tampered image is a different measurement: the launch is refused outright, and every later state would derive useless keys anyway.",
  },
  {
    id: "S1",
    name: "Guest agent starts",
    status: "live",
    summary:
      "The only software running is the audited, reproducibly built image: kernel, minimal userland, and the Postflight guest agent. Nothing Guardian could quietly swap in survives measurement.",
    hostSees: "Encrypted RAM.",
  },
  {
    id: "S2",
    name: "Key derivation",
    status: "design",
    summary:
      "The agent asks the PSP for a sealing key derived from the chip-unique secret, the launch measurement, and the guest policy. The chip secret is fused into the CPU and never leaves it — not to the guest OS, not to the host, not to Guardian. There is no check here to bypass: an inauthentic environment does not fail, it derives a different key that can decrypt nothing.",
    hostSees: "Nothing — the exchange happens inside protected RAM.",
  },
  {
    id: "S3",
    name: "Cache attach",
    status: "partial",
    summary:
      "The host hot-attaches volumes cached from this repository's previous runs. Every block it has been storing is ciphertext sealed in an earlier S9.",
    hostSees: "Ciphertext blocks, volume sizes.",
  },
  {
    id: "S4",
    name: "Unlock and verify",
    status: "design",
    summary:
      "The agent opens the volume with the derived key, then verifies an integrity tag computed with that same key over the cached contents. The tag must be keyed: a host that can corrupt blocks could also recompute a plain checksum, so an unkeyed hash would prove nothing.",
    hostSees: "Ciphertext blocks.",
    onFailure:
      "Tag mismatch: the cache is discarded and the run proceeds cold. Corruption can cost speed; it cannot cost confidentiality, and a corrupted snapshot is never restored into a running process.",
  },
  {
    id: "S5",
    name: "Warm thaw — or cold build",
    status: "partial",
    summary:
      "With a verified cache, the agent restores the warm process tree checkpointed after the last successful run: toolchains, package caches, build daemons. Without one, it builds the environment fresh and the job still runs.",
    hostSees: "Encrypted RAM, ciphertext blocks.",
  },
  {
    id: "S6",
    name: "Ready",
    status: "live",
    summary:
      "The warm pre-job state: a hot environment holding no credentials, no job identity, and no connection to GitHub. This is the only state that is ever checkpointed.",
    hostSees: "Encrypted RAM, ciphertext blocks.",
  },
  {
    id: "S7",
    name: "Register",
    status: "live",
    summary:
      "A single-use just-in-time runner credential is delivered for exactly one job. The runner dials GitHub fresh and registers as ephemeral. Credentials exist only in this state and die with the job — they are never present in any checkpoint or cache.",
    hostSees: "Encrypted RAM, ciphertext blocks, traffic timing.",
  },
  {
    id: "S8",
    name: "Job runs",
    status: "partial",
    summary:
      "Customer code executes. Plaintext exists in exactly two places: RAM that SEV-SNP encrypts with a key the host never holds, and filesystems mounted inside the guest on the encrypted volumes.",
    hostSees: "Encrypted RAM, ciphertext blocks, traffic timing and volume.",
  },
  {
    id: "S9",
    name: "Seal",
    status: "design",
    summary:
      "The runner exits and its credential is already dead. The agent checkpoints the warm process tree and workspace onto the encrypted volume, writes the keyed integrity tag, and unmounts.",
    hostSees: "Ciphertext blocks being written.",
  },
  {
    id: "S10",
    name: "Retire",
    status: "partial",
    summary:
      "The VM is destroyed. The host snapshots the volume — still ciphertext — and tags it with scheduling metadata: repository, image generation, sizes, timestamps. Metadata is the one thing Guardian does see.",
    hostSees: "Ciphertext snapshots, scheduling metadata.",
  },
] as const;

export const LIFECYCLE_INVARIANT =
  "In every state, the host's complete view of a customer workload is: ciphertext blocks, encrypted RAM, and traffic metadata.";

export interface Corollary {
  readonly heading: string;
  readonly body: string;
}

// Consequences that fall out of deriving keys from (chip secret × measurement
// × policy) — the parts a security review asks about first.
export const COROLLARIES: readonly Corollary[] = [
  {
    heading: "Keys are pinned to the physical chip.",
    body: "A cache sealed on one machine only opens on that machine, because the chip secret is part of the derivation. Warm jobs are therefore scheduled with host affinity; a job that lands elsewhere simply runs cold.",
  },
  {
    heading: "Keys rotate with every image release.",
    body: "A new golden image is a new measurement, which is a new key — the entire existing cache becomes unreachable ciphertext. That is a feature: the cache is disposable by construction. Losing every key costs cold starts, never data. There is no key escrow, no recovery path, and nothing to subpoena.",
  },
  {
    heading: "Guardian can derive the same key. It doesn't matter.",
    body: "Guardian owns the hardware, so Guardian can boot another VM from the same image on the same chip and the PSP will hand it the identical key. That VM runs the same measured code the customer audited — code that does not disclose keys or plaintext. Every guarantee on this page therefore rests on one thing: the golden image is open source and reproducibly built, so a customer or an independent auditor can bind the measurement the hardware attests to the code they can read. A different image is a different measurement, different keys, and a failed verification — there is no quiet swap.",
  },
];

export const HONEST_LIMITS: readonly Corollary[] = [
  {
    heading: "GitHub sees what it always saw.",
    body: "Source fetched from GitHub, job logs, and artifacts uploaded to GitHub are visible to GitHub. This page is about what Guardian can see.",
  },
  {
    heading: "Metadata is visible.",
    body: "Traffic timing and volume, cache sizes, repository identity, and scheduling history are visible to the host. Confidentiality covers content, not existence.",
  },
  {
    heading: "Availability is not covered.",
    body: "A compromised platform can refuse to run jobs or corrupt caches. The design turns that into slowness or downtime — verified by the keyed integrity check — never into disclosure.",
  },
];

export interface ObligationRow {
  readonly obligation: string;
  readonly status: MechanismStatus;
  readonly detail: string;
}

// The three-part obligation behind "we provably can't see your code",
// tracked honestly. The page ships before all three are true.
export const OBLIGATIONS: readonly ObligationRow[] = [
  {
    obligation: "Runtime memory confidentiality",
    status: "live",
    detail: "Every worker is an SEV-SNP guest; the host is cryptographically locked out of guest RAM.",
  },
  {
    obligation: "Customer-verifiable attestation",
    status: "partial",
    detail: "Hardware attestation reports work today; the customer-facing request-and-verify flow is not exposed yet.",
  },
  {
    obligation: "At-rest sealing of caches and checkpoints",
    status: "design",
    detail: "The key-derivation, encrypted-volume, and keyed-integrity design on this page. In active development — until it ships, cached data is protected by access control, not by cryptography.",
  },
];
