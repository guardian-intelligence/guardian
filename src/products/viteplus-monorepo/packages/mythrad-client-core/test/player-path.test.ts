// The player path: a session that owns a dog, sends intents, and has to
// reconcile its own predictions against the journal. Spectator probes
// never exercise any of it — they have no intents to predict, nothing to
// ack, and no join to race — so these invariants need their own suite.

import { describe, expect, it } from "vitest";
import { Emit, ResyncReason } from "../src/ports.ts";
import { Role, type ClientFrame } from "../src/wire.ts";
import { Ev, Reject, dogPayload, rig } from "./wasm.ts";

/** Ring entries land one per second, so this is the window after a restore with none. */
const RING_CADENCE_TICKS = 24;

function joinFrames(r: Awaited<ReturnType<typeof rig>>) {
  return r.harness.transport
    .sentFrames()
    .filter((f) => f.kind === "intent" && f.value.kind === Ev.join);
}

function idOf(frame: ClientFrame | undefined): bigint {
  return frame?.kind === "intent" ? frame.value.id : 0n;
}

function intentFrames(r: Awaited<ReturnType<typeof rig>>, kind: number) {
  return r.harness.transport
    .sentFrames()
    .filter((f) => f.kind === "intent" && f.value.kind === kind);
}

describe("a restored world can still absorb a late event", () => {
  it("rolls back to the restored state rather than asking for another snapshot", async () => {
    // A snapshot is a state at a known tick, so it is a rollback target
    // like any other. An event stamped between that tick and where the
    // replica has optimistically stepped to is repairable from it, and
    // repairing it locally is the whole point of holding the state.
    const r = await rig({ role: "player" });
    await r.establish();
    const restoredAt = r.core.state.tick;
    const resyncsBefore = Number(r.core.state.resyncs);
    const rollbacksBefore = r.core.state.rollbacks;

    // Step a few ticks — but stay inside one ring cadence, so the only
    // state old enough to roll back to is the snapshot itself.
    await r.run(200);
    expect(Number(r.core.state.tick - restoredAt)).toBeLessThan(RING_CADENCE_TICKS);
    expect(r.core.state.tick).toBeGreaterThan(restoredAt);

    // An event stamped at the restored tick: reachable, by definition.
    r.deliver([r.authority.frame(r.core.state.seq + 1n, restoredAt, Ev.join, dogPayload(0xa11n))]);
    expect(await r.until(() => r.core.state.events > 0, 1000)).toBe(true);
    expect(r.core.state.rollbacks).toBeGreaterThan(rollbacksBefore);
    expect(Number(r.core.state.resyncs)).toBe(resyncsBefore);
  });

  it("does not answer one snapshot by asking for the next", async () => {
    // The shape that turns a single late event into a loop: each resync
    // lands a snapshot, and a snapshot that cannot be rolled back to
    // leaves the next late event with nothing to repair from either.
    const r = await rig({ role: "player" });
    await r.establish();
    const resyncsBefore = Number(r.core.state.resyncs);

    for (let i = 0; i < 3; i++) {
      await r.run(150);
      r.deliver([
        r.authority.frame(
          r.core.state.seq + 1n,
          r.core.state.tick - 2n,
          Ev.join,
          dogPayload(BigInt(0xb00 + i)),
        ),
      ]);
      await r.run(150);
    }
    expect(Number(r.core.state.resyncs)).toBe(resyncsBefore);
    const reasons = r.harness.emitted
      .filter((e) => e.code === Emit.resyncRequested)
      .map((e) => Number(e.a));
    expect(reasons).not.toContain(ResyncReason.lateEvent);
  });
});

describe("a join is sent once", () => {
  it("puts exactly one join on the wire per connection", async () => {
    // The dog enters the park once. A second copy of the same intent is
    // answered "already present", which is a refusal the session then has
    // to explain away rather than an outcome anyone wanted.
    const r = await rig({ role: "player" });
    await r.establish();
    await r.run(300);
    expect(joinFrames(r)).toHaveLength(1);
  });

  it("keeps one join across a redial rather than minting another", async () => {
    // A reconnect resends what the park has not answered; it does not ask
    // for a second dog. Two joins under two identities are two arrivals as
    // far as the authority is concerned, and only one of them can succeed.
    const r = await rig({ role: "player", myDog: 0x5152n });
    await r.establish();
    await r.run(200);
    expect(new Set(joinFrames(r).map(idOf)).size).toBe(1);

    r.harness.transport.drop();
    expect(await r.until(() => r.harness.transport.dials > 1, 3000, 25)).toBe(true);
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);
    await r.run(300);

    expect(new Set(joinFrames(r).map(idOf)).size).toBe(1);
  });

  it("says nothing to the user when the park answers a join it already has", async () => {
    // "Already present" answering our own join describes the state we
    // asked for, so it is not news. That holds however many copies of the
    // answer arrive, including ones we can no longer match to an intent.
    const r = await rig({ role: "player", myDog: 0x5150n });
    await r.establish();
    await r.run(200);
    const join = joinFrames(r)[0];
    const id = join!.kind === "intent" ? join!.value.id : 0n;

    r.deliver([r.authority.reject(id, Reject.present)]);
    r.deliver([r.authority.reject(id, Reject.present)]);
    await r.run(200);

    const rejects = r.harness.emitted.filter((e) => e.code === Emit.reject);
    expect(rejects).toHaveLength(0);
    expect(r.harness.logs.filter((l) => /already/i.test(l))).toHaveLength(0);
  });

  it("keeps the dog in the presented world when the park says it is present", async () => {
    // The refusal and the outcome agree: our dog is in the park. The
    // prediction was right, so the presented world must not lose it.
    const r = await rig({ role: "player", myDog: 0x5151n });
    await r.establish();
    await r.run(200);
    const join = joinFrames(r)[0];
    const id = join!.kind === "intent" ? join!.value.id : 0n;
    r.deliver([r.authority.reject(id, Reject.present)]);
    await r.run(200);
    expect(r.core.state.present).toBe(true);
  });
});

describe("intents that need a dog in the park wait for one", () => {
  it("holds a boost until the join it depends on is acknowledged", async () => {
    // A boost describes a dog that is already inside. Sent while the join
    // is still unanswered it can only be refused, and the refusal costs a
    // round trip and an auto-rejoin to undo.
    const r = await rig({ role: "player", myDog: 0x6001n });
    await r.establish();
    await r.run(100);
    expect(r.core.state.seq).toBe(0n);

    r.core.setBoost(true);
    await r.run(200);
    expect(intentFrames(r, Ev.boostSet)).toHaveLength(0);

    // Once the journal confirms the dog, the held intent may go.
    const join = joinFrames(r)[0];
    const id = join!.kind === "intent" ? join!.value.id : 0n;
    r.deliver([r.emit(Ev.join, dogPayload(0x6001n), id)]);
    expect(await r.until(() => r.core.state.seq === r.authority.seq, 2000)).toBe(true);
    await r.run(200);
    expect(intentFrames(r, Ev.boostSet)).toHaveLength(1);
  });

  it("never earns an absent refusal on a fresh connection", async () => {
    // Nothing the client sends before its dog is in the park can succeed,
    // so nothing should be sent that needs it.
    const r = await rig({ role: "player", myDog: 0x6002n });
    await r.establish();
    r.core.setBoost(true);
    r.core.checkIn();
    r.core.moveTo(5);
    await r.run(300);

    const needsPresence = [Ev.boostSet, Ev.checkIn, Ev.moveTo].flatMap((k) => intentFrames(r, k));
    expect(needsPresence).toHaveLength(0);
  });
});
