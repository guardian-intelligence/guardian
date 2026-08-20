# Chunkies recovery drills

The six drills are slice C's definition of done (design of record
c2e3dbe9 §9): each one runs **for real, in prod, during the shadow
window** — PG is still the recovery authority, so a failed drill costs
a graph, not a world. Do not run any drill after the flip (slice D)
without treating this document as stale.

Scope: the WUM chunkie on `ash-worker0`, whose durability volume is the
second NVMe (serial `362510FCEFD5`, Talos user volume `chunkies`,
mounted into the pod from PV `ash-worker0-chunkies`). One chunkie, one
chunk, today.

## Preconditions (all drills)

1. The shadow lane is enabled and healthy: `WAL_DIR` set on the chunkie,
   `chunkies_wal_shadow_dead == 0`, `chunkies_wal_faults_total` flat,
   `chunkies_wal_durable_lag_seconds` sub-second, and the checkpoint
   cadence live (`chunkies_ckpt_last_unix` advancing, ring populated
   under `<WAL_DIR>/checkpoints/<chunk>/`).
2. The slice-B soak is clean: multi-day `wal_durable_lag`, faults=0, and
   the WAL≡PG comparator (replay vs `aspect mythra dump`) green.
3. A browser spectator session is open on the prod park
   (`agent-browser open https://wakeupmythra.com`) — every drill's pass
   criteria include a client-side observation.
4. Announce the drill on ntfy before starting; watch Alerta for the
   ~15-minute trailing window after.

The boot-time rehearsal is the observer for most drills: every chunkie
activation runs the full ladder against the volume between the writer
lock and its fresh WAL, and reports
`chunkies_recovery_rehearsals_total{outcome}` plus
`chunkies_recovery_rehearsal_loss_ticks`. A drill "passes" when the
rehearsal after the induced failure lands on the expected rung with the
expected loss, and the client sees what the wire promises.

## Drill 1 — Crash

SIGKILL the chunkie mid-traffic.

    kubectl -n tenant-guardian-prod exec deploy/chunkies-chunkie -- kill -9 1

Expected: the replacement's rehearsal reports `clean` (torn tail
truncated), `rehearsal_loss_ticks` ≤ one group commit (~20ms of ticks —
at 24Hz that is 0 or 1), the journal diff confirms the loss bound, and
the spectator session redials and resyncs without a reload.

## Drill 2 — Node reboot

Full ladder including flock re-acquisition and clock_skip repayment.

    talosctl -n <ash-worker0 ip> reboot

Expected: pod reschedules onto the same node (node-pinned), rehearsal
runs the full ladder on a cold volume, PG boot repays the frozen
wall-clock as one journaled clock_skip, spectator reconnects to a world
whose clock jumped once (no tick-by-tick catchup crawl).

## Drill 3 — Fenced takeover

Wedge the old pod, force a replacement; the old generation's writes
must be provably refused — no split brain.

    kubectl -n tenant-guardian-prod exec deploy/chunkies-chunkie -- kill -STOP 1
    kubectl -n tenant-guardian-prod delete pod -l app=chunkies-chunkie --force --grace-period=0

Expected: the replacement's `ticklog.Acquire` refuses with `ErrHeld`
while the wedged process lives (log: "serving unshadowed",
`chunkies_wal_shadow_dead == 1` on the replacement) and acquires
cleanly once the predecessor is truly dead; the volume shows no
interleaved generations (scan clean at next rehearsal); at no point do
two writers append.

## Drill 4 — Corrupt tail

Inject damage-with-survivors into the newest WAL segment (a few non-zero
bytes overwritten mid-segment, tail left intact), then restart the pod.

Expected: rehearsal refuses the WAL loudly and reports
`checkpoint_only`, `rehearsal_loss_ticks` ≤ the cadence (~20s of
ticks), and the log names the corrupt segment. Post-flip this is the
rung that rewinds lineage end-to-end at the client (resync, never a
spliced false world); during the shadow window the wire stays PG's, so
the client-side observation is deferred to the slice-D re-run of this
drill.

## Drill 5 — Checkpoint-only recovery

Destroy the WAL segments (not the checkpoint directory), restart.

    kubectl ... exec ... -- sh -c 'rm /var/mnt/chunkies/*.chkw'

Expected: rehearsal reports `checkpoint_only` from the ring's newest
proven manifest; loss ≤ 20s; the ring's oldest kept checkpoint would
also have sufficed (verify all K=3 are present and the newest is
`.ok`-marked).

## Drill 6 — Epoch-barrier promotion

A real module promotion through §6's ordering, plus a kill between
steps 2 and 3 to prove the crash story.

1. Ship a no-op sim module change through the normal mount flow; watch
   the soak, the epoch_advance, and `AdvanceEpoch`'s segment rotation
   (no segment spans the barrier — new segment ordinal at the swap).
2. Re-run with a SIGKILL immediately after the swap commits but before
   the next checkpoint cadence: the rehearsal must resume under the OLD
   pair's newest checkpoint (pair-match skips any newer manifest the
   dead epoch left) and replay cleanly across the barrier segments.

Expected additionally: `chunkies_epoch_swaps_total{result="committed"}`
increments, spectator rides through both promotions without a reload.

## Recording

Each drill: date, commit/OCI hash, rehearsal outcome counters before
and after, loss ticks, Alerta silence confirmation, and the browser
observation. File the six results in the PR that closes slice C's gate
— they are the evidence the flip (slice D) is allowed to cite.
