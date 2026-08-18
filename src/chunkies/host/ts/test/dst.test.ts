// The deterministic simulation suite: a real `ReplicaHost` driving the
// real committed modules, with a real park instance playing the authority, and
// only the network and the clock faked. Each case names the design-doc
// invariant it certifies.
//
// The network misbehaves on purpose here — frames split mid-varint,
// events arriving out of order or twice, datagrams lost and reordered,
// connections dying mid-frame — because those are the conditions the
// netcode exists to survive, and none of them are reachable from a
// well-behaved integration test.

import { describe, expect, it } from "vitest";
import { Emit, HostEmit, ResyncReason, type HostState } from "../src/index.ts";
import {
  bringTheDogIn,
  decodeCheck,
  dogPayload,
  encodeEventRecord,
  encodeTick,
  epochAdvancePayload,
  Ev,
  intentId,
  intentsSent,
  modules,
  Reject,
  rig,
  Role,
  strangerModule,
  type ClientFrame,
} from "@guardian/chunkies-testkit";

/** One tick at the fixture park's 24Hz, in milliseconds. */
const TICK_MS = 1000 / 24;
/** The ring holds one entry per second; a rollback needs at least one. */
const RING_WARMUP_MS = 3000;
/** Ten entries at one per second: past this, the oldest retained state is gone. */
const RING_DEPTH_MS = 10_000;
/** The cadence the session core repeats an unanswered resync request on. */
const RESYNC_RETRY_MS = 2000;

/** Resync requests the core has written, oldest first. */
function resyncFrames(r: Awaited<ReturnType<typeof rig>>): ClientFrame[] {
  return r.harness.transport.sentFrames().filter((f) => f.kind === "resync");
}

function haveSeqOf(frame: ClientFrame): bigint {
  return frame.kind === "resync" ? frame.value.haveSeq : -1n;
}

/**
 * Resync requests written since the last dial. Every connection opens with
 * a hello, so that frame is where one stream ends and the next begins.
 */
function resyncsOnLatestStream(r: Awaited<ReturnType<typeof rig>>): number {
  const sent = r.harness.transport.sentFrames();
  const from = sent.map((f) => f.kind).lastIndexOf("hello");
  return sent.slice(from).filter((f) => f.kind === "resync").length;
}

/**
 * Runs the session while answering its checks, so a long stretch stays
 * quiet: unanswered checks age out into strikes, and two strikes are a
 * resync of their own — which would satisfy a case about resyncs without
 * having anything to do with it.
 */
async function runAnsweringChecks(r: Awaited<ReturnType<typeof rig>>, ms: number): Promise<void> {
  for (let t = 0; t < ms; t += 500) {
    await r.run(Math.min(500, ms - t), 32);
    r.answerChecks();
  }
}

describe("boot and handshake", () => {
  it("lands a world: welcome, terrain fetch, snapshot", async () => {
    const r = await rig();
    await r.establish();
    expect(r.state.hz).toBe(24);
    expect(r.state.seq).toBe(r.authority.seq);
    expect(r.harness.blobFetches).toEqual([r.authority.terrainHex]);
    expect(r.codes()).toContain(Emit.snapshotRestored);
  });

  it("seeds the module word at boot, before dialing", async () => {
    const r = await rig();
    // module_swapped lands during boot, while no world is live, so it
    // announces the running module without disturbing any state.
    expect(r.count(Emit.moduleSwapped)).toBe(1);
    expect(r.state.replicaModuleWord).toMatch(/^[0-9a-f]{8}$/);
    expect(r.count(Emit.snapshotRestored)).toBe(0);
  });

  it("sends its own join — the host must not", async () => {
    const r = await rig({ role: "player" });
    await r.establish();
    const sent = r.harness.transport.sentFrames();
    expect(sent[0]!.kind).toBe("hello");
    // The core sends its own join from `session_connected`; the host has
    // no part in it. How many frames that one intent is worth on the wire
    // is a separate invariant, in the player-path suite.
    const joins = sent.filter((f) => f.kind === "intent" && f.value.kind === Ev.join);
    expect(joins.length).toBeGreaterThanOrEqual(1);
  });

  it("a spectator sends no join at all", async () => {
    const r = await rig({ role: "spectator" });
    await r.establish(Role.spectator);
    const sent = r.harness.transport.sentFrames();
    expect(sent.filter((f) => f.kind === "intent")).toHaveLength(0);
    expect(r.state.role).toBe("spectator");
  });

  it("takes the granted role from the welcome, over the one it asked for", async () => {
    // A token that no longer proves a player comes back a spectator; the
    // welcome is authoritative and must overwrite what init was told.
    const r = await rig({ role: "player" });
    await r.establish(Role.spectator);
    expect(r.state.role).toBe("spectator");
  });

  it("adopts a journaled live rate on the same connection and world", async () => {
    const r = await rig();
    await r.establish();
    await r.run(500);
    const connected = r.count(Emit.connectedHelloSent);
    const restored = r.count(Emit.snapshotRestored);
    const resynced = r.count(Emit.resyncRequested);
    const before = r.state.tick;
    const boundary = r.authority.tick;
    const payload = new Uint8Array(4);
    new DataView(payload.buffer).setUint32(0, 48, true);

    r.deliver([r.authority.apply(Ev.rateSet, payload)]);
    expect(await r.until(() => r.state.seq === r.authority.seq)).toBe(true);
    expect(r.state.hz).toBe(48);
    expect(r.count(Emit.rateChanged)).toBe(1);
    expect(r.harness.emitted.filter((e) => e.code === Emit.rateChanged).at(-1)).toMatchObject({
      a: boundary,
      b: (24n << 32n) | 48n,
    });

    await r.run(500);
    expect(r.state.tick).toBeGreaterThan(before);
    expect(r.count(Emit.connectedHelloSent)).toBe(connected);
    expect(r.count(Emit.snapshotRestored)).toBe(restored);
    expect(r.count(Emit.resyncRequested)).toBe(resynced);
    expect(r.harness.logs).toContain(`rate: 24Hz -> 48Hz at tick ${boundary}`);
  });
});

describe("invariant 2: seq-dense application", () => {
  it("applies events in seq order at their tick", async () => {
    const r = await rig();
    await r.establish();
    const before = r.state.dogCount;
    r.deliver([r.emit(Ev.join, dogPayload(0xa1n)), r.emit(Ev.join, dogPayload(0xa2n))]);
    expect(await r.until(() => r.state.seq === r.authority.seq)).toBe(true);
    expect(r.state.dogCount).toBe(before + 2);
    expect(Number(r.state.resyncs)).toBe(0);
  });

  it("holds a later event until the gap ahead of it is filled", async () => {
    const r = await rig();
    await r.establish();
    const first = r.emit(Ev.join, dogPayload(0xb1n), 0n, 40);
    const second = r.emit(Ev.join, dogPayload(0xb2n), 0n, 60);
    const seqBefore = r.state.seq;

    r.deliver([second]);
    await r.run(300);
    // seq 2 cannot apply over a missing seq 1, however long it waits.
    expect(r.state.seq).toBe(seqBefore);

    r.deliver([first]);
    expect(await r.until(() => r.state.seq === r.authority.seq)).toBe(true);
    expect(Number(r.state.resyncs)).toBe(0);
  });

  it("ignores a duplicated event", async () => {
    const r = await rig();
    await r.establish();
    const before = r.state.dogCount;
    const frame = r.emit(Ev.join, dogPayload(0xc1n));
    r.deliver([frame, frame, frame]);
    expect(await r.until(() => r.state.seq === r.authority.seq)).toBe(true);
    await r.run(300);
    expect(r.state.dogCount).toBe(before + 1);
    expect(r.state.events).toBe(1);
  });

  it("ignores an event whose seq it has already passed", async () => {
    const r = await rig();
    await r.establish();
    const frame = r.emit(Ev.join, dogPayload(0xd1n));
    r.deliver([frame]);
    expect(await r.until(() => r.state.seq === r.authority.seq)).toBe(true);
    const stale = r.authority.frame(r.state.seq, r.state.tick, Ev.join, dogPayload(0xd2n));
    r.deliver([stale]);
    await r.run(300);
    expect(r.state.events).toBe(1);
    expect(Number(r.state.resyncs)).toBe(0);
  });

  it("survives a burst delivered in reverse order", async () => {
    const r = await rig();
    await r.establish();
    const frames = [0xe1n, 0xe2n, 0xe3n, 0xe4n].map((id) => r.emit(Ev.join, dogPayload(id)));
    r.deliver([...frames].reverse());
    expect(await r.until(() => r.state.seq === r.authority.seq)).toBe(true);
    expect(r.state.events).toBe(4);
    expect(Number(r.state.resyncs)).toBe(0);
  });
});

describe("invariant 2/7: rollback", () => {
  it("a late event inside the ring rolls back, applies, and does NOT resync", async () => {
    // A resync costs a full snapshot; the ring exists so that mere
    // lateness never pays that price (src/chunkies/README.md invariant 2).
    const r = await rig();
    await r.establish();
    await r.run(RING_WARMUP_MS);
    const at = r.state.tick - 5n;
    expect(at).toBeGreaterThan(24n);
    const rollbacksBefore = r.state.rollbacks;
    const resyncsBefore = r.state.resyncs;

    r.deliver([r.authority.frame(r.state.seq + 1n, at, Ev.join, dogPayload(0xf1n))]);
    expect(await r.until(() => r.state.rollbacks > rollbacksBefore, 500)).toBe(true);
    expect(r.state.events).toBeGreaterThan(0);
    expect(r.state.resyncs).toBe(resyncsBefore);
    expect(r.codes()).toContain(Emit.rollback);
  });

  it("never shows the world at an earlier tick than it already showed", async () => {
    // A rollback is a repair, and a repair is not something the viewer is
    // supposed to watch. Rewinding and replaying belong to the same frame:
    // whatever the replica had reached, it still has when that frame ends.
    const r = await rig();
    await r.establish();
    await r.run(RING_WARMUP_MS);
    const was = r.state.tick;
    r.deliver([r.authority.frame(r.state.seq + 1n, was - 4n, Ev.join, dogPayload(0xf2n))]);
    expect(await r.until(() => r.state.rollbacks > 0, 500)).toBe(true);
    expect(r.state.tick).toBeGreaterThanOrEqual(was);
  });

  it("never shows the world earlier after a restore either", async () => {
    // A snapshot describes the world at the tick it was taken, which can
    // be behind where the replica has already presented. Landing it is a
    // repair like any other: the tick the viewer has been shown is a
    // floor, and the restore owes it back before the frame ends.
    const r = await rig();
    await r.establish();
    // Taken now, delivered much later — the authority's answer to a
    // resync is always a description of a moment already passing.
    const stale = r.authority.snapshot();
    await r.run(1500);

    const seen: bigint[] = [];
    const watch = () => {
      seen.push(r.state.tick);
    };
    watch();
    const high = seen[0]!;
    const restores = r.count(Emit.snapshotRestored);
    r.deliver([stale]);
    for (let t = 0; t < 600; t += 16) {
      r.harness.clock.advance(16);
      r.pump();
      watch();
      await r.harness.settle();
    }

    expect(r.count(Emit.snapshotRestored)).toBeGreaterThan(restores);
    for (let i = 1; i < seen.length; i++) {
      expect(seen[i], `frame ${i} went backwards`).toBeGreaterThanOrEqual(seen[i - 1]!);
    }
    expect(seen.at(-1)).toBeGreaterThanOrEqual(high);
  });

  it("a late event deeper than the ring resyncs instead", async () => {
    // The ring is finite, so far enough back is unreachable and only a
    // snapshot can place the event. Reaching that depth means outrunning
    // every retained state, including the one the last restore left.
    const r = await rig();
    await r.establish();
    const restoredAt = r.state.tick;
    // Long enough that cadence pushes have evicted the restored floor.
    await r.run(RING_DEPTH_MS + RING_WARMUP_MS);
    const resyncsBefore = r.state.resyncs;
    r.deliver([r.authority.frame(r.state.seq + 1n, restoredAt, Ev.join, dogPayload(0xf3n))]);
    expect(await r.until(() => r.state.resyncs > resyncsBefore, 500)).toBe(true);
    const resync = r.harness.emitted.find((e) => e.code === Emit.resyncRequested);
    expect(Number(resync!.a)).toBe(ResyncReason.lateEvent);
  });

  it("refuses a rollback that would silently drop a newer event, and resyncs", async () => {
    // The authority stamps ticks monotonically with seq. An event that
    // breaks that would be dropped by the replay, so the repair is
    // refused rather than performed wrongly.
    const r = await rig();
    await r.establish();
    await r.run(RING_WARMUP_MS);
    const base = r.state.seq;
    const now = r.state.tick;
    // seq+1 stamped far in the future, then seq+2 stamped in the past:
    // rolling back for seq+2 would strand seq+1's replay.
    r.deliver([r.authority.frame(base + 1n, now + 2n, Ev.join, dogPayload(0x11n))]);
    expect(await r.until(() => r.state.seq === base + 1n, 500)).toBe(true);
    const resyncsBefore = r.state.resyncs;
    r.deliver([r.authority.frame(base + 2n, 2n, Ev.join, dogPayload(0x12n))]);
    expect(await r.until(() => r.state.resyncs > resyncsBefore, 500)).toBe(true);
  });
});

describe("invariant 3: the ring entry is the state at entry to a tick", () => {
  it("a check about a tick both replicas reached is judged ok", async () => {
    // The property exists so a same-tick check samples the state before
    // that tick's events. If the client re-hashed after applying them,
    // this verdict would come back a mismatch.
    const r = await rig({ checkMs: 200 });
    await r.establish();
    r.deliver([r.emit(Ev.join, dogPayload(0x21n))]);
    expect(await r.until(() => r.state.seq === r.authority.seq)).toBe(true);
    await r.run(2000);
    const checks = r.harness.transport.sentDatagrams;
    expect(checks.length).toBeGreaterThan(0);
    const check = decodeCheck(checks.at(-1)!);
    // The authority is kept in lockstep, so at the same tick it holds the
    // same hash the client sent.
    expect(check.tick).toBeLessThanOrEqual(r.authority.tick);
    expect(r.state.mismatches).toBe(0);
  });
});

describe("invariant 4: two strikes, one resync", () => {
  it("one mismatch does not resync; the second does, exactly once", async () => {
    const r = await rig({ checkMs: 150 });
    await r.establish();
    await r.waitForChecks(1);
    expect(r.answerChecks({ ok: false })).toBe(1);
    await r.run(100);
    // One strike is a hiccup, not a divergence.
    expect(Number(r.state.resyncs)).toBe(0);

    await r.waitForChecks(2);
    expect(r.answerChecks({ ok: false })).toBe(1);
    await r.run(200);
    expect(Number(r.state.resyncs)).toBe(1);
    expect(r.state.mismatches).toBeGreaterThanOrEqual(2);
    const resync = r.harness.emitted.find((e) => e.code === Emit.resyncRequested);
    expect(Number(resync!.a)).toBe(ResyncReason.hashMismatch);
  });

  it("an ok verdict resets the strike count", async () => {
    const r = await rig({ checkMs: 150 });
    await r.establish();
    await r.waitForChecks(1);
    r.answerChecks({ ok: false });
    await r.waitForChecks(2);
    r.answerChecks({ ok: true });
    await r.waitForChecks(3);
    r.answerChecks({ ok: false });
    await r.run(300);
    // Two mismatches separated by an ok are one strike each, never two.
    expect(Number(r.state.resyncs)).toBe(0);
  });

  it("loses nothing to a snapshot that answers a resync", async () => {
    // A snapshot answering a resync is queued into the same ordered stream
    // as the events, so everything sent before it arrived ahead of it and
    // is folded into the state it carries. That is what entitles the core
    // to drop whatever it still had queued when one lands — the events are
    // not discarded, they are already IN there.
    //
    // The failure this pins is silent and total: a core that drops the
    // queue without the snapshot containing those events loses them for
    // good, and the only symptom is a park that is quietly missing dogs.
    const r = await rig({ checkMs: 150, myDog: 0x9401n });
    await r.establish();
    await bringTheDogIn(r, 0x9401n);
    const before = r.state.dogCount;

    // Two strikes: the core asks for a snapshot and holds everything.
    await r.waitForChecks(1);
    r.answerChecks({ ok: false });
    await r.waitForChecks(2);
    r.answerChecks({ ok: false });
    expect(await r.until(() => r.state.resyncs > 0, 2000)).toBe(true);
    expect(r.pump().resyncing).toBe(true);

    // Dogs keep arriving while it waits. These reach the client before the
    // answer does, so they are exactly what a queue-clearing restore would
    // throw away.
    for (let i = 0; i < 3; i++) r.deliver([r.emit(Ev.join, dogPayload(BigInt(0x9410 + i)))]);
    await r.run(100);

    // Coverage: they must still be UNAPPLIED when the answer goes out, or
    // this case is watching three ordinary joins land and proving nothing.
    expect(r.state.dogCount, "dogs still held back").toBe(before);
    expect(r.state.seq, "events still queued").toBeLessThan(r.authority.seq);
    expect(r.answerResyncs(), "resync requests answered").toBe(1);
    expect(await r.until(() => r.state.seq === r.authority.seq, 4000)).toBe(true);

    // Nothing lost: the three that arrived mid-resync are in the world the
    // snapshot brought, even though the queue holding them was cleared.
    expect(r.state.dogCount, "dogs after the restore").toBe(before + 3);
    expect(r.state.resyncs, "one resync, not a loop").toBe(1);
  });

  it("asks again when a resync goes unanswered", async () => {
    // A resync request is one frame on a stream, and a frame can be lost
    // with the connection still up — nothing about the transport will tell
    // the session so. Asking once and waiting leaves a session that has
    // already declared its own state unusable sitting quiet forever: the
    // replica does not step, no event applies, and the only thing that
    // ever moves again is the user reloading the page.
    const r = await rig();
    await r.establish();
    // Long enough that the restored floor has been evicted, so the event
    // below is genuinely out of reach — and quiet throughout, so the only
    // resync in this case is the one it triggers on purpose.
    await runAnsweringChecks(r, RING_DEPTH_MS + RING_WARMUP_MS);
    expect(r.state.resyncs, "clean before the trigger").toBe(0);
    expect(r.state.mismatches, "clean before the trigger").toBe(0);

    r.deliver([r.authority.frame(r.state.seq + 1n, 1n, Ev.join, dogPayload(0xf7n))]);
    expect(await r.until(() => r.state.resyncs > 0, 1000)).toBe(true);
    const asked = resyncFrames(r).length;
    expect(asked, "the first request went out").toBeGreaterThan(0);

    // Nothing answers it. The session must keep asking.
    await runAnsweringChecks(r, RESYNC_RETRY_MS * 2);
    const again = resyncFrames(r);
    expect(again.length, "requests sent while the first went unanswered").toBeGreaterThan(asked);
    // Each one describes the same gap, because nothing has landed to
    // change it: a request that renumbers itself is asking for a different
    // snapshot than the one it still needs.
    expect(new Set(again.map(haveSeqOf)).size, "every request describes the same gap").toBe(1);
    // And they are that first request repeated, not new trouble — a second
    // cause would satisfy the count above while proving nothing.
    const reasons = new Set(
      r.harness.emitted.filter((e) => e.code === Emit.resyncRequested).map((e) => Number(e.a)),
    );
    expect([...reasons], "one cause, asked about repeatedly").toEqual([ResyncReason.lateEvent]);
  });

  it("a check aged out of the server ring counts as a strike", async () => {
    const r = await rig({ checkMs: 150 });
    await r.establish();
    await r.waitForChecks(1);
    r.answerChecks({ known: false });
    await r.waitForChecks(2);
    r.answerChecks({ known: false });
    await r.run(300);
    expect(Number(r.state.resyncs)).toBeGreaterThan(0);
    const resync = r.harness.emitted.find((e) => e.code === Emit.resyncRequested);
    expect(Number(resync!.a)).toBe(ResyncReason.checkAgedOut);
  });
});

describe("invariant 5: snapshot restore", () => {
  it("waits for terrain before restoring, then lands", async () => {
    const r = await rig();
    r.deliver([r.authority.welcome()]);
    await r.until(() => r.state.hz > 0, 200);
    r.deliver([r.authority.snapshot()]);
    // The fetch is in flight: the snapshot is held, not dropped.
    expect(r.harness.blobFetches).toEqual([r.authority.terrainHex]);
    expect(await r.until(() => r.count(Emit.snapshotRestored) > 0, 2000)).toBe(true);
    expect(r.state.tick).toBeGreaterThan(0n);
  });

  it("clears the event queue so a stale gap cannot wedge the session", async () => {
    const r = await rig();
    await r.establish();
    // Queue an event behind a gap that will never be filled.
    r.deliver([r.authority.frame(r.state.seq + 5n, r.state.tick, Ev.join, dogPayload(1n))]);
    await r.run(300);
    r.authority.step(4);
    r.deliver([r.authority.snapshot()]);
    expect(await r.until(() => r.state.seq === r.authority.seq, 1000)).toBe(true);
    // Fresh events apply straight away after the restore.
    r.deliver([r.emit(Ev.join, dogPayload(0x31n))]);
    expect(await r.until(() => r.state.seq === r.authority.seq, 1000)).toBe(true);
  });

  it("carries unanswered intents onto the next connection", async () => {
    // A connection that died with an intent outstanding never got it to
    // the park, so the next one has to say it again. Saying it twice on
    // the SAME connection is not a resend, it is a duplicate.
    const r = await rig({ role: "player", myDog: 0x88n });
    await r.establish();
    await bringTheDogIn(r, 0x88n);

    r.checkIn();
    await r.run(150);
    expect(intentsSent(r, Ev.checkIn)).toHaveLength(1);
    const id = intentId(intentsSent(r, Ev.checkIn)[0]);

    r.harness.transport.drop();
    expect(await r.until(() => r.harness.transport.dials > 1, 3000, 25)).toBe(true);
    r.deliver([r.authority.welcome(Role.player), r.authority.snapshot()]);
    await r.run(200);

    // The park already holds the dog, so it answers the reconnecting
    // join with "already present" — which is what re-establishes the
    // presence the held intent was waiting on.
    const rejoin = intentId(intentsSent(r, Ev.join).at(-1));
    r.deliver([r.authority.reject(rejoin, Reject.present)]);
    await r.run(300);

    const sent = intentsSent(r, Ev.checkIn);
    expect(sent).toHaveLength(2);
    // The same intent, said again — not a new one.
    expect(intentId(sent[1])).toBe(id);
  });

  it("reports a hash disagreement on the restored state", async () => {
    const r = await rig();
    await r.establish();
    const before = r.state.mismatches;
    r.deliver([r.authority.snapshotWithBadHash()]);
    expect(await r.until(() => r.state.mismatches > before, 1000)).toBe(true);
    expect(r.codes()).toContain(Emit.mismatch);
  });
});

describe("invariant 6: a restore against the wrong world is surfaced, not looped", () => {
  it("reports park code 4 once and does not retry it", async () => {
    const r = await rig();
    await r.establish();
    r.deliver([r.authority.snapshotWithWrongEmbeddedTerrain()]);
    expect(await r.until(() => r.count(Emit.restoreFailed) > 0, 1000)).toBe(true);
    const failed = r.harness.emitted.find((e) => e.code === Emit.restoreFailed);
    expect(Number(failed!.a)).toBe(4);
    // A wrong world cannot be fixed by asking again: no retry storm.
    const failures = r.count(Emit.restoreFailed);
    await r.run(6000);
    expect(r.count(Emit.restoreFailed)).toBe(failures);
  });

  it("a terrain fetch that fails retries rather than wedging", async () => {
    // The opposite case: a transient fetch failure IS worth another go.
    const r = await rig({ withoutTerrain: true });
    r.deliver([r.authority.welcome()]);
    await r.run(200);
    r.deliver([r.authority.snapshot()]);
    await r.run(5000);
    expect(r.harness.blobFetches.length).toBeGreaterThan(1);
    expect(r.state.seq).toBe(0n);
  });
});

describe("invariant 7: module epoch", () => {
  it("an epoch_advance event asks for the module exactly once", async () => {
    const r = await rig();
    await r.establish();
    const fetchesBefore = r.harness.moduleFetches.length;
    r.deliver([r.emit(Ev.epochAdvance, epochAdvancePayload(2, 0xdeadbeefn))]);
    expect(await r.until(() => r.count(Emit.moduleSwapWanted) > 0, 1000)).toBe(true);
    await r.run(1000);
    expect(r.count(Emit.moduleSwapWanted)).toBe(1);
    expect(r.harness.moduleFetches.length).toBe(fetchesBefore + 1);
  });

  it("a repeated epoch_advance does not re-latch while one is in flight", async () => {
    const r = await rig();
    await r.establish();
    r.deliver([r.emit(Ev.epochAdvance, epochAdvancePayload(2, 0xdeadbeefn))]);
    await r.run(100);
    r.deliver([r.emit(Ev.epochAdvance, epochAdvancePayload(3, 0xdeadbeefn))]);
    await r.run(1500);
    expect(r.count(Emit.moduleSwapWanted)).toBeLessThanOrEqual(2);
  });

  it("the swap lands into fresh instances and resyncs into them", async () => {
    const r = await rig();
    await r.establish();
    r.deliver([r.emit(Ev.epochAdvance, epochAdvancePayload(2, 0xdeadbeefn))]);
    expect(await r.until(() => r.count(Emit.moduleSwapped) > 1, 2000)).toBe(true);
    // Fresh instances hold no world, so the core drops state and asks for
    // a snapshot to rebuild into them.
    const swapped = () =>
      r.harness.emitted.some(
        (e) => e.code === Emit.resyncRequested && Number(e.a) === ResyncReason.moduleSwapped,
      );
    expect(await r.until(swapped, 2000)).toBe(true);
    // The first resync names module_epoch, raised the moment the swap is
    // wanted; this one names module_swapped and is the one that rebuilds
    // the world into the instances that just replaced the old ones.
    const reasons = r.harness.emitted
      .filter((e) => e.code === Emit.resyncRequested)
      .map((e) => Number(e.a));
    expect(reasons).toContain(ResyncReason.moduleEpoch);
    expect(reasons).toContain(ResyncReason.moduleSwapped);
    r.authority.epoch = 2;
    r.deliver([r.authority.snapshot()]);
    expect(await r.until(() => r.state.seq === r.authority.seq, 2000)).toBe(true);
  });

  it("a swap that cannot load its terrain leaves the old world running", async () => {
    // A new module refusing the terrain we hold is a real shape — the
    // schema tightening is exactly the sort of change that ships with a
    // module. Publishing the fresh instances before their terrain loaded
    // would leave a worldless instance live, `session_module_swapped`
    // never called, and every retry failing identically: a frozen park.
    const r = await rig();
    await r.establish();
    await r.run(400);
    const tickBefore = r.state.tick;
    const dogsBefore = r.state.dogCount;

    // Serve a "new module" that cannot take our world: valid wasm with no
    // park surface, so the swap dies at the ABI check — before a
    // worldless instance can be published — which lands in the same catch
    // as a terrain refusal would.
    r.harness.setModule("replica", strangerModule().slice().buffer);
    r.deliver([r.emit(Ev.epochAdvance, epochAdvancePayload(2, 0xdeadbeefn))]);
    expect(await r.until(() => r.harness.logs.some((l) => l.includes("swap failed")), 2000)).toBe(
      true,
    );

    // The retired module is still running the world: the replica keeps
    // stepping and the roster is intact.
    await r.run(600);
    expect(r.state.tick).toBeGreaterThan(tickBefore);
    expect(r.state.dogCount).toBe(dogsBefore);
    expect(r.pump().haveState).toBe(true);

    // And the retry lane recovers once the module serves a loadable world.
    r.harness.setModule("replica", modules().park.slice().buffer);
    expect(await r.until(() => r.count(Emit.moduleSwapped) > 1, 8000)).toBe(true);
  });

  it("a verdict naming a different park module asks for the swap", async () => {
    const r = await rig({ checkMs: 150 });
    await r.establish();
    await r.run(1800);
    const wanted = r.count(Emit.moduleSwapWanted);
    expect(r.answerChecks({ pw: Uint8Array.of(9, 9, 9, 9) })).toBeGreaterThan(0);
    await r.run(300);
    expect(r.count(Emit.moduleSwapWanted)).toBeGreaterThan(wanted);
  });
});

describe("invariant 8: intents and their answers", () => {
  it("applies an answered join once, not twice", async () => {
    // The join we sent and the journal event announcing it describe one
    // arrival. Counting them separately would put two dogs in the park.
    const r = await rig({ role: "player", myDog: 0x777n });
    await r.establish();
    await r.run(100);
    const sent = r.harness.transport.sentFrames();
    const join = sent.find((f) => f.kind === "intent" && f.value.kind === Ev.join);
    const id = join!.kind === "intent" ? join!.value.intent : 0n;
    r.deliver([r.emit(Ev.join, dogPayload(0x777n), id)]);
    expect(await r.until(() => r.state.seq === r.authority.seq)).toBe(true);
    expect(r.state.present).toBe(true);
    expect(r.state.dogCount).toBe(1);
  });

  it("swallows the two rejects that describe the state we asked for", async () => {
    const r = await rig({ role: "player", myDog: 0x779n });
    await r.establish();
    await r.run(100);
    const join = r.harness.transport
      .sentFrames()
      .find((f) => f.kind === "intent" && f.value.kind === Ev.join);
    const id = join!.kind === "intent" ? join!.value.intent : 0n;
    // "Already present" answering a join IS the joined state.
    r.deliver([r.authority.reject(id, Reject.present)]);
    await r.run(200);
    expect(r.codes()).not.toContain(Emit.reject);
    expect(r.state.rejects).toBe(1);
  });

  it("re-joins once when the park says our dog is absent", async () => {
    // "Absent" answering anything but a join means the park lost the dog
    // we believed was in it — a departure that raced us. Going back in is
    // the repair, and once per window is the limit, because if the park
    // keeps saying absent then re-joining is not working.
    const r = await rig({ role: "player", myDog: 0x77an });
    await r.establish();
    await bringTheDogIn(r, 0x77an);
    const joinsBefore = new Set(intentsSent(r, Ev.join).map(intentId)).size;

    r.checkIn();
    await r.run(150);
    const id = intentId(intentsSent(r, Ev.checkIn)[0]);
    r.deliver([r.authority.reject(id, Reject.absent)]);
    await r.run(300);

    expect(r.codes()).toContain(Emit.autoRejoin);
    const ids = new Set(intentsSent(r, Ev.join).map(intentId));
    expect(ids.size).toBe(joinsBefore + 1);
  });
});

describe("invariant 9: an idle session sends only checks", () => {
  it("writes nothing to the stream once established", async () => {
    const r = await rig({ checkMs: 200, role: "spectator" });
    await r.establish(Role.spectator);
    await r.run(1600);
    const framesBefore = r.harness.transport.sentStream.length;
    const bytesBefore = r.state.bytesDown;
    await r.run(3000);
    expect(r.harness.transport.sentStream.length).toBe(framesBefore);
    expect(r.harness.transport.sentDatagrams.length).toBeGreaterThan(0);
    // Nothing arrives either: the replica steps locally, at zero cost.
    expect(r.state.bytesDown).toBe(bytesBefore);
  });

  it("goes silent while hidden and resumes when visible", async () => {
    const r = await rig({ checkMs: 200 });
    await r.establish();
    await r.run(1800);
    expect(r.harness.transport.sentDatagrams.length).toBeGreaterThan(0);
    r.setVisible(false);
    const quiet = r.harness.transport.sentDatagrams.length;
    await r.run(2000);
    expect(r.harness.transport.sentDatagrams.length).toBe(quiet);
    r.setVisible(true);
    await r.run(600);
    expect(r.harness.transport.sentDatagrams.length).toBeGreaterThan(quiet);
  });
});

describe("network abuse", () => {
  it("reassembles a session delivered one byte at a time", async () => {
    const r = await rig();
    const ones = Array.from({ length: 4000 }, () => 1);
    r.deliver([r.authority.welcome()], ones);
    await r.until(() => r.state.hz > 0, 500);
    r.deliver([r.authority.snapshot()], ones);
    expect(await r.until(() => r.state.seq === r.authority.seq, 3000)).toBe(true);
  });

  it("survives a read boundary inside the length prefix of a big frame", async () => {
    const r = await rig();
    await r.establish();
    const frame = r.authority.snapshot();
    // Cut after the first byte of a two-byte varint, then dribble.
    r.deliver([frame], [1, 1, 7, 300]);
    expect(await r.until(() => r.count(Emit.snapshotRestored) > 1, 2000)).toBe(true);
  });

  it("carries several frames and half of another in one read", async () => {
    const r = await rig();
    await r.establish();
    const a = r.emit(Ev.join, dogPayload(0x41n));
    const b = r.emit(Ev.join, dogPayload(0x42n));
    const c = r.emit(Ev.join, dogPayload(0x43n));
    const joined = new Uint8Array(a.length + b.length + c.length);
    joined.set(a, 0);
    joined.set(b, a.length);
    joined.set(c, a.length + b.length);
    r.harness.transport.deliverStream(joined, [a.length + b.length + 3]);
    expect(await r.until(() => r.state.seq === r.authority.seq, 1000)).toBe(true);
    expect(r.state.events).toBe(3);
  });

  it("ignores a lost verdict and keeps checking", async () => {
    const r = await rig({ checkMs: 150 });
    await r.establish();
    await r.run(1800);
    const sent = r.harness.transport.sentDatagrams.length;
    // Answer nothing at all: loss is the datagram contract.
    await r.run(2000);
    expect(r.harness.transport.sentDatagrams.length).toBeGreaterThan(sent);
    expect(Number(r.state.resyncs)).toBe(0);
  });

  it("tolerates a verdict arriving after a newer one", async () => {
    const r = await rig({ checkMs: 150 });
    await r.establish();
    await r.run(2200);
    const checks = r.harness.transport.sentDatagrams.map((d) => decodeCheck(d));
    expect(checks.length).toBeGreaterThan(1);
    for (const check of [...checks].reverse()) {
      r.harness.transport.deliverDatagram(r.authority.verdict(check, { ok: true }));
    }
    await r.run(300);
    expect(Number(r.state.resyncs)).toBe(0);
  });

  it("reassembles the largest snapshot a park can emit", async () => {
    // The biggest thing on the wire. A park's whole state lives in its
    // 64 KiB io buffer, so a full 2048-dog roster is ~61.5 KB; sent as
    // stored blocks the frame is about that size, which is larger than
    // the host's staging buffer and forces #onStreamBytes to split it.
    const r = await rig();
    await r.establish();
    expect(r.authority.fillRoster()).toBe(2048);
    const frame = r.authority.snapshotUncompressed();
    expect(frame.length).toBeGreaterThan(60_000);

    const restores = r.count(Emit.snapshotRestored);
    // One read, larger than session_cap, cut nowhere convenient.
    r.harness.transport.deliverStream(frame);
    expect(await r.until(() => r.count(Emit.snapshotRestored) > restores, 4000)).toBe(true);
    // The restore lands inside the stream call; the world is
    // rebuilt on the next pump, which is where the roster becomes visible.
    expect(await r.until(() => r.state.dogCount === 2048, 2000)).toBe(true);
    expect(r.state.seq).toBe(r.authority.seq);
    expect(Number(r.state.mismatches)).toBe(0);
  });

  it("reassembles that snapshot however the reads are cut", async () => {
    const r = await rig();
    await r.establish();
    r.authority.fillRoster();
    const frame = r.authority.snapshotUncompressed();
    const restores = r.count(Emit.snapshotRestored);
    // A ragged delivery: a byte, the rest of the prefix, then odd sizes
    // that land mid-payload and straddle the host's staging boundary.
    r.harness.transport.deliverStream(frame, [1, 2, 3, 60_000, 999]);
    expect(await r.until(() => r.count(Emit.snapshotRestored) > restores, 4000)).toBe(true);
    expect(await r.until(() => r.state.dogCount === 2048, 2000)).toBe(true);
  });

  it("tears the transport down when reassembly overflows, and redials", async () => {
    // Overflow needs a partial frame already buffered plus a large read
    // behind it — no single frame can do it. The core drops the buffer
    // and asks the host to close, because byte alignment is gone and a
    // resync written onto this stream would be answered into garbage.
    const r = await rig();
    await r.establish();
    const dials = r.harness.transport.dials;

    // Claim a frame far larger than anything real, then feed it. The
    // core buffers it happily until pending + incoming passes the cap.
    const header = new Uint8Array(4);
    new DataView(header.buffer).setUint32(0, 0x8000_0000 | 400_000, false);
    r.harness.transport.deliverStream(header);
    const filler = new Uint8Array(64 * 1024);
    for (let i = 0; i < 4 && r.harness.transport.dials === dials; i++) {
      r.harness.transport.deliverStream(filler);
      await r.harness.settle();
    }

    expect(r.harness.logs.some((l) => l.includes("framing lost"))).toBe(true);
    expect(r.harness.transport.closed).toBeGreaterThan(0);
    expect(
      r.harness.emitted.some(
        // With the caps table shared, a declared over-cap frame is a framing
        // violation the moment its prefix is readable — earlier than the
        // v4-era buffer-overflow path, same teardown.
        (e) => e.code === HostEmit.teardown && e.a === BigInt(ResyncReason.framing),
      ),
    ).toBe(true);
    expect(await r.until(() => r.harness.transport.dials > dials, 8000, 25)).toBe(true);

    // And the replacement connection is a working session: the replica
    // survived, and a fresh snapshot lands on the new stream.
    const restores = r.count(Emit.snapshotRestored);
    r.deliver([r.authority.snapshot()]);
    expect(await r.until(() => r.count(Emit.snapshotRestored) > restores, 3000)).toBe(true);
    expect(r.pump().haveState).toBe(true);
  });

  it("tears down on a length prefix it cannot read", async () => {
    const r = await rig();
    await r.establish();
    const dials = r.harness.transport.dials;
    // A frame declaring zero length: there is no kind byte and no way to
    // step past it, so the decoder can never make progress. Reason 12,
    // distinct from the overflow's 11.
    r.harness.transport.deliverStream(Uint8Array.of(0x00));
    await r.harness.settle();
    expect(r.harness.logs.some((l) => l.includes("framing lost"))).toBe(true);
    expect(
      r.harness.emitted.some(
        (e) => e.code === HostEmit.teardown && e.a === BigInt(ResyncReason.framing),
      ),
    ).toBe(true);
    expect(await r.until(() => r.harness.transport.dials > dials, 8000, 25)).toBe(true);
  });

  it("ignores a datagram that is not a verdict", async () => {
    const r = await rig();
    await r.establish();
    r.harness.transport.deliverDatagram(new Uint8Array(34));
    r.harness.transport.deliverDatagram(Uint8Array.of(1, 2, 3));
    await r.run(200);
    expect(r.state.seq).toBe(r.authority.seq);
  });

  it("refuses an event payload past the core's cap and resyncs", async () => {
    const r = await rig();
    await r.establish();
    const resyncsBefore = r.state.resyncs;
    const oversize = encodeTick(r.state.tick, r.state.seq + 1n, [
      encodeEventRecord(0n, Ev.join, 0x1n, new Uint8Array(65)),
    ]);
    r.deliver([oversize]);
    expect(await r.until(() => r.state.resyncs > resyncsBefore, 500)).toBe(true);
    const resync = r.harness.emitted.filter((e) => e.code === Emit.resyncRequested).at(-1);
    expect(Number(resync!.a)).toBe(ResyncReason.queueOverflow);
  });
});

describe("connection lifecycle", () => {
  it("redials on a doubling backoff to a 5000ms ceiling", async () => {
    const r = await rig({ seed: 3 });
    await r.establish();
    // Every dial from here on fails, so the backoff escalates instead of
    // being reset by a success.
    r.harness.transport.outcomes = [{ ok: false, error: "handshake timeout" }];
    const waits: number[] = [];
    r.harness.transport.drop();
    for (let i = 0; i < 6; i++) {
      const dials = r.harness.transport.dials;
      const at = r.harness.clock.now();
      // The redial is a timer, so the wait is observable as clock time.
      await r.until(() => r.harness.transport.dials > dials, 8000, 25);
      waits.push(r.harness.clock.now() - at);
    }
    expect(waits[0]).toBeLessThan(600);
    expect(waits[1]).toBeGreaterThan(waits[0]!);
    // Jitter is bounded, so the ceiling holds with room for it.
    expect(Math.max(...waits)).toBeLessThan(5000 + 250 + 100);
    expect(waits.at(-1)).toBeGreaterThan(2000);
  });

  it("reports the backoff it chose, which the core cannot know", async () => {
    const r = await rig();
    await r.establish();
    r.harness.transport.outcomes = [{ ok: false, error: "handshake timeout" }];
    r.harness.transport.drop();
    await r.until(() => r.harness.transport.dials > 3, 20_000, 25);
    const redials = r.harness.emitted.filter((e) => e.code === HostEmit.redial);
    expect(redials.length).toBeGreaterThan(2);
    // Host codes start at 1000 so no consumer can confuse them with the
    // session core's 1..16.
    expect(HostEmit.redial).toBeGreaterThanOrEqual(1000);
    expect(r.codes().filter((c) => c > 0 && c <= 16).length).toBeGreaterThan(0);
    const waits = redials.map((e) => Number(e.a));
    expect(waits[0]).toBeGreaterThanOrEqual(300);
    expect(Math.max(...waits)).toBeLessThanOrEqual(5000 + 250);
    // b carries the consecutive-failure count, which climbs while dials fail.
    expect(Number(redials.at(-1)!.b)).toBeGreaterThan(Number(redials[0]!.b));
  });

  it("tells the core the transport died, so a half-frame cannot survive it", async () => {
    const r = await rig();
    await r.establish();
    const frame = r.authority.snapshot();
    // Half a snapshot, then the connection dies mid-frame.
    r.deliver([frame], [40]);
    r.harness.transport.drop();
    await r.until(() => r.harness.transport.dials > 1, 3000, 25);
    // The next connection starts clean: a whole frame is read correctly
    // rather than being appended to the orphaned prefix.
    const restores = r.count(Emit.snapshotRestored);
    r.deliver([r.authority.snapshot()]);
    expect(await r.until(() => r.count(Emit.snapshotRestored) > restores, 2000)).toBe(true);
  });

  it("a drop mid-resync does not wedge the latch on the next connection", async () => {
    // The regression this pins: without `session_disconnected` the core
    // still believes it is connected and still holds `resyncing`, so
    // every later request_resync no-ops on the new stream — silently, and
    // for the rest of the session.
    const r = await rig({ checkMs: 150 });
    await r.establish();

    await r.waitForChecks(1);
    r.answerChecks({ ok: false });
    await r.waitForChecks(2);
    r.answerChecks({ ok: false });
    await r.run(200);
    expect(resyncFrames(r).length).toBeGreaterThan(0);
    // Still outstanding: no snapshot has been delivered to answer it.
    expect(r.pump().resyncing).toBe(true);

    r.harness.transport.drop();
    expect(await r.until(() => r.harness.transport.dials > 1, 3000, 25)).toBe(true);
    expect(r.pump().resyncing).toBe(false);

    // The same two strikes must be able to ask again, ON THE NEW STREAM —
    // which is the only place the answer can be read. Counting requests
    // across the whole session would not say that: a session that repeats
    // an unanswered request would run the count up on the dead stream and
    // look identical to one that recovered.
    await r.waitForChecks(3);
    r.answerChecks({ ok: false });
    await r.waitForChecks(4);
    r.answerChecks({ ok: false });
    await r.run(200);
    expect(resyncsOnLatestStream(r), "requests written to the connection that can answer").toBe(1);
  });

  it("keeps the replica across a redial", async () => {
    const r = await rig();
    await r.establish();
    await r.run(1000);
    const seq = r.state.seq;
    const tick = r.state.tick;
    r.harness.transport.drop();
    await r.until(() => r.harness.transport.dials > 1, 3000, 25);
    expect(r.state.seq).toBe(seq);
    expect(r.state.tick).toBeGreaterThanOrEqual(tick);
    // The hello carries what we already have, so the server can catch us up.
    const hellos = r.harness.transport.sentFrames().filter((f) => f.kind === "hello");
    expect(hellos).toHaveLength(2);
    expect(hellos[1]!.kind === "hello" && hellos[1]!.value.sinceSeq).toBe(seq);
  });

  it("reidentify redials under the new dog and clears the old intents", async () => {
    const r = await rig({ role: "spectator", myDog: 0x1n });
    await r.establish(Role.spectator);
    const dials = r.harness.transport.dials;
    r.reidentify(0x999n, "player");
    expect(r.state.role).toBe("player");
    await r.until(() => r.harness.transport.dials > dials, 2000, 25);
    // The new connection joins as the new dog.
    r.deliver([r.authority.welcome(Role.player)]);
    await r.run(200);
    const joins = r.harness.transport
      .sentFrames()
      .filter((f) => f.kind === "intent" && f.value.kind === Ev.join);
    expect(joins.length).toBeGreaterThan(0);
    const last = joins.at(-1)!;
    expect(last.kind === "intent" && last.value.actor).toBe(0x999n);
  });
});

describe("a zero step budget: observe, do not step", () => {
  it("lets the clock escalate and resync while the replica stands still", async () => {
    // What a debug freeze needs. Skipping `pump` entirely stops the
    // session dead and freezes the very readouts the freeze exists to
    // watch; a zero budget starves only the stepping, so the clock keeps
    // observing, escalates, and eventually demands a snapshot on screen.
    const r = await rig({ checkMs: 200 });
    await r.establish();
    await r.run(600);

    const frozenAt = r.state.tick;
    const startMs = r.harness.clock.now();
    const startTick = r.authority.tick;
    // The world keeps running while we are frozen; the authority is the
    // only thing still moving, so the clock learns it is falling behind.
    const worldRunsOn = () => {
      const target = startTick + BigInt(Math.floor((r.harness.clock.now() - startMs) / TICK_MS));
      while (r.authority.tick < target) r.authority.step();
    };

    let status = r.pump(0);
    let everStepped = false;
    for (let t = 0; t < 90_000 && status.clock !== "snapshotRequired"; t += 50) {
      r.harness.clock.advance(50);
      worldRunsOn();
      status = r.pump(0);
      everStepped ||= status.stepped;
      // Answer honestly-ok so the ONLY thing driving a resync here is the
      // clock, not a hash the frozen replica cannot possibly match.
      r.answerChecks({ ok: true });
      await r.harness.settle();
    }

    expect(status.clock).toBe("snapshotRequired");
    expect(everStepped).toBe(false);
    expect(r.state.tick).toBe(frozenAt);
    expect(Number(r.state.resyncs)).toBeGreaterThan(0);
    const reasons = r.harness.emitted
      .filter((e) => e.code === Emit.resyncRequested)
      .map((e) => Number(e.a));
    expect(reasons).toContain(ResyncReason.clock);

    // Resume: the snapshot the core asked for lands and the replica is
    // back with the world, at the authority's tick rather than its own.
    r.deliver([r.authority.snapshot()]);
    expect(await r.until(() => r.state.tick > frozenAt, 3000)).toBe(true);
    expect(r.state.tick).toBeGreaterThan(startTick);
    expect(r.pump().clock).not.toBe("snapshotRequired");
  });

  it("still applies journal events while frozen", async () => {
    // "Observe" includes keeping up with the journal: only stepping is
    // starved, so an event stamped at a tick already reached still lands.
    const r = await rig();
    await r.establish();
    await r.run(400);
    const at = r.state.tick;
    const before = r.state.events;
    r.deliver([r.authority.frame(r.state.seq + 1n, at, Ev.join, dogPayload(0x91n))]);
    for (let t = 0; t < 500; t += 50) {
      r.harness.clock.advance(50);
      r.pump(0);
      await r.harness.settle();
    }
    expect(r.state.events).toBe(before + 1);
    expect(r.state.tick).toBe(at);
  });

  it("restores normal stepping when the budget comes back", async () => {
    const r = await rig();
    await r.establish();
    await r.run(400);
    const frozenAt = r.state.tick;
    for (let t = 0; t < 1000; t += 50) {
      r.harness.clock.advance(50);
      r.pump(0);
      await r.harness.settle();
    }
    expect(r.state.tick).toBe(frozenAt);
    await r.run(1000);
    expect(r.state.tick).toBeGreaterThan(frozenAt);
  });
});

describe("pump status and the read surface", () => {
  it("reports have_state only once a world has landed", async () => {
    const r = await rig();
    expect(r.pump().haveState).toBe(false);
    await r.establish();
    expect(r.pump().haveState).toBe(true);
  });

  it("reports the clock state the session disciplines", async () => {
    const r = await rig();
    await r.establish();
    await r.run(1000);
    const { clock } = r.pump();
    // The read surface must take it from the same place, not from the
    // module's standalone clock export, which tracks a different Clock.
    expect(r.state.clockState).toBe(clock);
    expect(clock).not.toBe("acquiring");
  });

  // The render alpha is only meaningful if the clock producing it is
  // the one the session disciplines — a phase that never moves renders
  // every dog frozen between ticks.
  it("advances the render phase between ticks", async () => {
    const r = await rig();
    await r.establish();
    const phases = new Set<number>();
    for (let i = 0; i < 8; i++) {
      await r.run(10, 5);
      phases.add(r.host.smoothFrame(r.harness.clock.now()).phaseQ16);
    }
    expect(phases.size).toBeGreaterThan(1);
    expect(Math.max(...phases)).toBeLessThanOrEqual(65536);
  });

  it("notifies a subscriber only when the state changes", async () => {
    const r = await rig();
    const seen: HostState[] = [];
    r.host.subscribe((s) => seen.push(s));
    await r.establish();
    await r.run(500);
    expect(seen.length).toBeGreaterThan(1);
    for (let i = 1; i < seen.length; i++) {
      expect(seen[i], `notification ${i} changed nothing`).not.toEqual(seen[i - 1]);
    }
    expect(seen.at(-1)!.phase).toBe("live");
    expect(seen.at(-1)!.rateHz).toBe(24);
  });

  it("serves one live diagnostics record and state agrees with it", async () => {
    const r = await rig();
    await r.establish();
    r.deliver([r.emit(Ev.join, dogPayload(0x61n))]);
    await r.until(() => r.state.seq === r.authority.seq);
    const d = r.diag();
    expect(d).not.toBeNull();
    // the record is the source ClientState refreshes from, and it names
    // its own invariant: the trail target rides the bytes, not a mirror
    expect(d!.events).toBe(1);
    expect(d!.tick).toBe(r.state.tick);
    expect(d!.seq).toBe(r.state.seq);
    expect(d!.clockState).toBe(r.state.clockState);
    expect(d!.trailTargetTicks).toBeGreaterThan(0);
    expect(d!.cushionTicks).toBeGreaterThanOrEqual(d!.trailTargetTicks);
    // trail measures from the authority's present, error from the
    // cushioned schedule: they must differ by exactly the cushion, which
    // pins every one of the three fields to its right offset
    expect(d!.trailTicks - d!.errorTicks).toBeCloseTo(d!.cushionTicks, 3);
    expect(r.state.bytesDown).toBeGreaterThan(0);
  });

  it("keeps the tick moving at roughly the park's rate", async () => {
    const r = await rig();
    await r.establish();
    const from = r.state.tick;
    await r.run(2000);
    const ticks = Number(r.state.tick - from);
    // The clock decides the pace; it must track 24Hz, not race or stall.
    expect(ticks).toBeGreaterThan(2000 / TICK_MS / 2);
    expect(ticks).toBeLessThan((2000 / TICK_MS) * 2);
  });
});
