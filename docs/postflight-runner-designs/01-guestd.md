# 01 — guestd

> **EPHEMERAL.** Delete with this directory when the implementation pass completes.

The agent inside every runner VM. It is the only process that talks to the
host, and the runner never starts until guestd says the guest is ready.

## Shape

- Static Go binary (`src/services/postflight/guestd/`), built like the other
  slim images (static-Go-on-empty doctrine; no TLS roots needed — vsock only).
- Started by systemd inside the guest (`guestd.service`, `Restart=no`:
  a dead guestd is a dead VM by design — hostd's probe fails and the slot is
  destroyed and refilled).
- Listens on vsock port 1 (guest CID assigned by hostd at launch; already in
  the QEMU golden argv). Wire format is `guestproto` JSON-lines, one
  connection at a time, host dials guest.

## Protocol (extends merged `hostd/guestproto`)

Existing verbs: `hello`, `assignment`, `runner-status`.

| Verb | Direction | Payload | Semantics |
| - | - | - | - |
| `hello` | guest→host on accept | guestd version, boot id | Liveness + identity probe |
| `assignment` | host→guest | JIT config blob, env map, mounts: `[{serial, fstype, mountpoint, options}]` | Idempotent. guestd converges every mount, writes env, then execs the runner. Re-delivery after a partial apply must converge, not error. |
| `runner-status` | guest→host, streamed | phase: `mounting` / `listening` / `job-started` / `exited{code}` | hostd folds these into lease reports |
| `quiesce` | host→guest | `{mountpoint}` | `sync` + unmount the workspace filesystem; reply `quiesced` or `quiesce-failed{reason}`. Precedes the host-side seal snapshot. |

## Mount convergence (the invariant)

**No customer step runs until every mount in the assignment has converged.**
guestd locates each disk by its device serial (hostd sets `serial=` on the
`scsi-hd` device — the tracer recipe), creates the filesystem if the device
is blank (first generation of a scope arrives as an empty zvol), mounts with
the requested options **always including `discard`** (TRIM must pass through
to the sparse zvol or NVMe accounting measures garbage retention), and only
then execs the runner. A mount that cannot converge within its deadline is
reported `runner-status: exited` with a synthetic failure code — hostd
destroys the slot; the job is never started against a partial workspace.

## Runner execution

- Runner tree is baked into the image at `/opt/actions-runner` (02).
- guestd writes the env map to the runner's environment, drops privileges to
  the `runner` user, and execs `run.sh --jitconfig <blob>`.
- The JIT config exists only in guest RAM and the runner's process
  environment. It is never written to any disk, including the workspace.
- Runner exit code is reported verbatim in `runner-status: exited`. Exit 0
  makes the workspace **seal-eligible**; it never decides promotion (GitHub's
  attempt-specific job conclusion does, control-plane side — see 04).

## Quiesce (why it exists)

The host snapshots the workspace zvol while the guest is still alive —
that is what lets the slot be released immediately instead of waiting for
GitHub's conclusion to propagate. A live guest holds dirty pages, so the
sequence is strict: runner exits → hostd sends `quiesce` → guestd syncs and
unmounts → hostd snapshots → VM destroyed. Any quiesce failure is reported,
skips the seal (ambiguity never promotes), and still destroys the VM.

## TEE seams (specified now, implemented in the SNP phase)

- `quiesce` reply gains `tree_hash` (computed in-guest; host is blind to
  plaintext under LUKS) and an SNP attestation report with
  `report_data = hash(generation_id, tree_hash)`.
- Mount step gains LUKS open with per-generation derived keys — blocked on
  the dedicated key-handling security review.

## Testing

- Unit: protocol handling against the merged fake transport; mount
  convergence against a loopback block device with injected failures
  (blank device, wrong fs, busy unmount).
- Conformance (tracer host): real vsock end-to-end with 03's transport —
  assignment → mounted → runner stub exec → exit → quiesce → host snapshot
  succeeds and the snapshot mounts clean on the host.
