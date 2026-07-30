# Postflight storage

Status: end-state architecture, 2026-07-24.

Customer warm state is a regenerable cache on node-local NVMe: encrypted
in-guest, sealed by evidence, promoted by CAS. Anything that would
complicate that is a cold build.

## Sticky disks

- One striped NVMe zpool per worker host. Workspace, tool, and process
  volumes are sparse zvols; disk is the only overcommitted dimension,
  bounded by refusal-only watermarks.
- The unit of warmth is the CoW clone: materializing a workspace from a
  sealed generation is constant-time metadata (~tens of milliseconds at any
  size; measurements in the benchmark records). Guests get volumes by stable
  device serial via virtio-scsi hot-attach.
- No network storage on any hot path, no Ceph, no durable object tier for
  customer state. A future cold-offload component would
  be an optimization, not a dependency.
- TRIM passes end to end (guest discard → zvol); accounting measures the
  working set, not garbage retention. Per-generation size and use metrics
  are recorded at seal and on clone.

## Scope

A scope is the cache identity a job reads and may write. The key carries the
full shape; under-keying causes cross-job pollution:

```text
scope = (tenant, repository, scope_ref,
         workflow identity, job identity, matrix identity,
         runner class, trust class, compat class)
```

- `scope_ref` for a pull request is the **target** branch: PRs read the
  trusted generation and their writes never promote into it.
- The compatibility class ([fleet](postflight-fleet.md)) bounds where the
  scope's process capsules can restore; scopes never span fleets.
- A missing or incompatible generation is an empty volume and a cold build,
  never an error. Cache state is not semantic truth.

## Generations

A generation is an authenticated tuple — workspace, root, and tool volume
snapshots plus an optional CRIU process capsule — under one manifest:

- Fields: component snapshot GUIDs and content digests, process capsule
  digest and CRIU format, parent lineage and monotonic generation number,
  platform and compatibility tuple, SNP measurement and minimum TCB
  (Confidential), key reference (derivation salt on Confidential; wrapped
  DEK on Turbo).
- Manifests are signed with the `postflight-manifest` Transit key; the
  private key never leaves OpenBao; verification is offline against the
  published public key.
- The process capsule is an optimization over the durable volumes and can be
  invalidated alone: CRIU incompatibility costs process warmth, not artifact
  warmth.

### The seal pipeline

Order is the security property: a partial or ambiguous sequence publishes
nothing and the previous pointer stays authoritative.

1. The attempt concludes; runner processes are killed and proven absent.
2. The capsule freezes; CRIU dumps into the encrypted process volume; every
   durable filesystem flushes.
3. The donor VM is destroyed.
4. The slot releases for refill (`slot_reusable`); sealing continues on
   Guardian's clock.
5. The workspace/tool/process tuple snapshots atomically in one txg; GUIDs
   are recorded.
6. The manifest is assembled and signed; the generation becomes a candidate.
7. Attempt-specific GitHub success promotes it via scope-pointer CAS against
   the observed prior generation. A lost race retains, a failed attempt
   discards, an unsafe restore quarantines with evidence.

## Locality

Warmth is host-affine:

- Confidential volume keys are chip-bound: a generation's ciphertext opens
  only on the chip that wrote it. Turbo generations are resident where
  sealed.
- A scope's home is the host where it last sealed. A job GitHub hands to a
  guest elsewhere runs cold there; its seal establishes the new residency.
  No transfer ever holds a customer's Worker.
- Host loss, class retirement, image rolls, and key rotation each cost the
  affected scopes one cold build. Nothing is replicated, migrated, or
  recovered; there is no key-release plane for moving warm state between
  chips. The manifest and catalog keep the shape portable warmth would need
  (key reference, lineage, pointer CAS): adopting it later — on measured
  pull only — changes the key plane, not the schema.

## Key custody

One custodian per fleet; details in the
[security model](postflight-security-model.md):

| | Confidential | Turbo |
| --- | --- | --- |
| Volume key | Derived in-guest: chip half (PSP, measurement-bound) + tenant half (`transit-postflight`) | Per-lineage DEK from a `transit-postflight` data key |
| Host sees | Ciphertext and sealed frames only | Ciphertext at rest; key transits to guest RAM at rendezvous |
| Crypto-erase | Delete the tenant Transit key (kills the tenant half fleet-wide) | Delete the tenant Transit key (wrapped DEKs die) |
| Rotation | New lineage, one cold build | New lineage, one cold build |

## Retention

- **Reap is a control-plane verb.** hostd never deletes a sealed generation
  on its own; it freely GCs derived state (scratch clones, dead VM disks,
  pack caches).
- Retention ranks by last use, size, and pins; it never destroys anything
  referenced by a pointer, an in-flight manifest, or a running operation.
  Rollback floors keep enough lineage for the freshness gate.
- Staleness is a tradeoff, not a cleaning policy: durable workspaces carry
  stale derived files across runs, and aggressive cleaning would erase the
  product. We work with customers on repo hygiene; a divergence canary
  (periodic cold re-run, compare conclusions, invalidate on mismatch) is a
  possible future feature.

Related: [architecture](postflight-architecture.md) ·
[fleet](postflight-fleet.md) · [security model](postflight-security-model.md) ·
[scheduling](postflight-scheduling.md)
