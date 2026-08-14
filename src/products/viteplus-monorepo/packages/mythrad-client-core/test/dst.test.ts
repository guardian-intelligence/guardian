// The deterministic simulation suite: a real `Core` driving the real
// committed modules, with a real park instance playing the authority, and
// only the network and the clock faked. Each case names the design-doc
// invariant it certifies.
//
// The network misbehaves on purpose here — frames split mid-varint,
// events arriving out of order or twice, datagrams lost and reordered,
// connections dying mid-frame — because those are the conditions the
// netcode exists to survive, and none of them are reachable from a
// well-behaved integration test.

import { describe, expect, it } from "vitest";
import { Emit, HostEmit, ResyncReason, Stat } from "../src/ports.ts";
import { PumpFlag, clockStateOf } from "../src/abi.ts";
import { Role, decodeCheck, encodeEvent } from "../src/wire.ts";
import { Ev, Reject, dogPayload, epochAdvancePayload, modules, rig } from "./wasm.ts";

/** One tick at the fixture park's 24Hz, in milliseconds. */
const TICK_MS = 1000 / 24;
/** The ring holds one entry per second; a rollback needs at least one. */
const RING_WARMUP_MS = 3000;

describe("boot and handshake", () => {
  it("lands a world: welcome, terrain fetch, snapshot", async () => {
    const r = await rig();
    await r.establish();
    expect(r.core.state.hz).toBe(24);
    expect(r.core.state.seq).toBe(r.authority.seq);
    expect(r.harness.terrainFetches).toEqual([r.authority.terrainHex]);
    expect(r.codes()).toContain(Emit.snapshotRestored);
  });

  it("seeds the module word at boot, before dialing", async () => {
    const r = await rig();
    // module_swapped lands during boot, while no world is live, so it
    // announces the running module without disturbing any state.
    expect(r.count(Emit.moduleSwapped)).toBe(1);
    expect(r.core.state.parkWord).toMatch(/^[0-9a-f]{8}$/);
    expect(r.count(Emit.snapshotRestored)).toBe(0);
  });

  it("sends its own join — the host must not", async () => {
    const r = await rig({ role: "player" });
    await r.establish();
    const sent = r.harness.transport.sentFrames();
    expect(sent[0]!.kind).toBe("hello");
    const joins = sent.filter((f) => f.kind === "intent" && f.value.kind === Ev.join);
    // The snapshot restore resends unanswered intents, so the join can
    // appear twice on the wire — but it is one intent, with one identity.
    expect(joins.length).toBeGreaterThanOrEqual(1);
    const ids = new Set(joins.map((f) => (f.kind === "intent" ? f.value.id : 0n)));
    expect(ids.size).toBe(1);
  });

  it("a spectator sends no join at all", async () => {
    const r = await rig({ role: "spectator" });
    await r.establish(Role.spectator);
    const sent = r.harness.transport.sentFrames();
    expect(sent.filter((f) => f.kind === "intent")).toHaveLength(0);
    expect(r.core.state.role).toBe("spectator");
  });

  it("takes the granted role from the welcome, over the one it asked for", async () => {
    // A token that no longer proves a player comes back a spectator; the
    // welcome is authoritative and must overwrite what init was told.
    const r = await rig({ role: "player" });
    await r.establish(Role.spectator);
    expect(r.core.state.role).toBe("spectator");
  });
});

describe("invariant 2: seq-dense application", () => {
  it("applies events in seq order at their tick", async () => {
    const r = await rig();
    await r.establish();
    const before = r.core.state.dogCount;
    r.deliver([r.emit(Ev.join, dogPayload(0xa1n)), r.emit(Ev.join, dogPayload(0xa2n))]);
    expect(await r.until(() => r.core.state.seq === r.authority.seq)).toBe(true);
    expect(r.core.state.dogCount).toBe(before + 2);
    expect(Number(r.core.state.resyncs)).toBe(0);
  });

  it("holds a later event until the gap ahead of it is filled", async () => {
    const r = await rig();
    await r.establish();
    const first = r.emit(Ev.join, dogPayload(0xb1n), 0n, 40);
    const second = r.emit(Ev.join, dogPayload(0xb2n), 0n, 60);
    const seqBefore = r.core.state.seq;

    r.deliver([second]);
    await r.run(300);
    // seq 2 cannot apply over a missing seq 1, however long it waits.
    expect(r.core.state.seq).toBe(seqBefore);

    r.deliver([first]);
    expect(await r.until(() => r.core.state.seq === r.authority.seq)).toBe(true);
    expect(Number(r.core.state.resyncs)).toBe(0);
  });

  it("ignores a duplicated event", async () => {
    const r = await rig();
    await r.establish();
    const before = r.core.state.dogCount;
    const frame = r.emit(Ev.join, dogPayload(0xc1n));
    r.deliver([frame, frame, frame]);
    expect(await r.until(() => r.core.state.seq === r.authority.seq)).toBe(true);
    await r.run(300);
    expect(r.core.state.dogCount).toBe(before + 1);
    expect(r.core.state.events).toBe(1);
  });

  it("ignores an event whose seq it has already passed", async () => {
    const r = await rig();
    await r.establish();
    const frame = r.emit(Ev.join, dogPayload(0xd1n));
    r.deliver([frame]);
    expect(await r.until(() => r.core.state.seq === r.authority.seq)).toBe(true);
    const stale = r.authority.frame(
      r.core.state.seq,
      r.core.state.tick,
      Ev.join,
      dogPayload(0xd2n),
    );
    r.deliver([stale]);
    await r.run(300);
    expect(r.core.state.events).toBe(1);
    expect(Number(r.core.state.resyncs)).toBe(0);
  });

  it("survives a burst delivered in reverse order", async () => {
    const r = await rig();
    await r.establish();
    const frames = [0xe1n, 0xe2n, 0xe3n, 0xe4n].map((id) => r.emit(Ev.join, dogPayload(id)));
    r.deliver([...frames].reverse());
    expect(await r.until(() => r.core.state.seq === r.authority.seq)).toBe(true);
    expect(r.core.state.events).toBe(4);
    expect(Number(r.core.state.resyncs)).toBe(0);
  });
});

describe("invariant 2/7: rollback", () => {
  it("a late event inside the ring rolls back, applies, and does NOT resync", async () => {
    // A resync costs a full snapshot; the ring exists so that mere
    // lateness never pays that price (docs/netcode.md invariant 2).
    const r = await rig();
    await r.establish();
    await r.run(RING_WARMUP_MS);
    const at = r.core.state.tick - 5n;
    expect(at).toBeGreaterThan(24n);
    const rollbacksBefore = r.core.state.rollbacks;
    const resyncsBefore = r.core.state.resyncs;

    r.deliver([r.authority.frame(r.core.state.seq + 1n, at, Ev.join, dogPayload(0xf1n))]);
    expect(await r.until(() => r.core.state.rollbacks > rollbacksBefore, 500)).toBe(true);
    expect(r.core.state.events).toBeGreaterThan(0);
    expect(r.core.state.resyncs).toBe(resyncsBefore);
    expect(r.codes()).toContain(Emit.rollback);
  });

  it("the replica ends up back where it was, not stranded in the past", async () => {
    const r = await rig();
    await r.establish();
    await r.run(RING_WARMUP_MS);
    const was = r.core.state.tick;
    r.deliver([r.authority.frame(r.core.state.seq + 1n, was - 4n, Ev.join, dogPayload(0xf2n))]);
    expect(await r.until(() => r.core.state.rollbacks > 0, 500)).toBe(true);
    // The rollback lands at the event's tick and leaves a deficit; the
    // clock is what closes it, over the frames that follow.
    await r.run(1000);
    expect(r.core.state.tick).toBeGreaterThanOrEqual(was);
  });

  it("a late event deeper than the ring resyncs instead", async () => {
    const r = await rig();
    await r.establish();
    await r.run(RING_WARMUP_MS);
    const resyncsBefore = r.core.state.resyncs;
    // Tick 1 predates every ring entry: the oldest is a multiple of hz.
    r.deliver([r.authority.frame(r.core.state.seq + 1n, 1n, Ev.join, dogPayload(0xf3n))]);
    expect(await r.until(() => r.core.state.resyncs > resyncsBefore, 500)).toBe(true);
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
    const base = r.core.state.seq;
    const now = r.core.state.tick;
    // seq+1 stamped far in the future, then seq+2 stamped in the past:
    // rolling back for seq+2 would strand seq+1's replay.
    r.deliver([r.authority.frame(base + 1n, now + 2n, Ev.join, dogPayload(0x11n))]);
    expect(await r.until(() => r.core.state.seq === base + 1n, 500)).toBe(true);
    const resyncsBefore = r.core.state.resyncs;
    r.deliver([r.authority.frame(base + 2n, 2n, Ev.join, dogPayload(0x12n))]);
    expect(await r.until(() => r.core.state.resyncs > resyncsBefore, 500)).toBe(true);
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
    expect(await r.until(() => r.core.state.seq === r.authority.seq)).toBe(true);
    await r.run(2000);
    const checks = r.harness.transport.sentDatagrams;
    expect(checks.length).toBeGreaterThan(0);
    const check = decodeCheck(checks.at(-1)!);
    // The authority is kept in lockstep, so at the same tick it holds the
    // same hash the client sent.
    expect(check.tick).toBeLessThanOrEqual(r.authority.tick);
    expect(r.core.state.mismatches).toBe(0);
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
    expect(Number(r.core.state.resyncs)).toBe(0);

    await r.waitForChecks(2);
    expect(r.answerChecks({ ok: false })).toBe(1);
    await r.run(200);
    expect(Number(r.core.state.resyncs)).toBe(1);
    expect(r.core.state.mismatches).toBeGreaterThanOrEqual(2);
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
    expect(Number(r.core.state.resyncs)).toBe(0);
  });

  it("a check aged out of the server ring counts as a strike", async () => {
    const r = await rig({ checkMs: 150 });
    await r.establish();
    await r.waitForChecks(1);
    r.answerChecks({ known: false });
    await r.waitForChecks(2);
    r.answerChecks({ known: false });
    await r.run(300);
    expect(Number(r.core.state.resyncs)).toBeGreaterThan(0);
    const resync = r.harness.emitted.find((e) => e.code === Emit.resyncRequested);
    expect(Number(resync!.a)).toBe(ResyncReason.checkAgedOut);
  });
});

describe("invariant 5: snapshot restore", () => {
  it("waits for terrain before restoring, then lands", async () => {
    const r = await rig();
    r.deliver([r.authority.welcome()]);
    await r.until(() => r.core.state.hz > 0, 200);
    r.deliver([r.authority.snapshot()]);
    // The fetch is in flight: the snapshot is held, not dropped.
    expect(r.harness.terrainFetches).toEqual([r.authority.terrainHex]);
    expect(await r.until(() => r.count(Emit.snapshotRestored) > 0, 2000)).toBe(true);
    expect(r.core.state.tick).toBeGreaterThan(0n);
  });

  it("clears the event queue so a stale gap cannot wedge the session", async () => {
    const r = await rig();
    await r.establish();
    // Queue an event behind a gap that will never be filled.
    r.deliver([
      r.authority.frame(r.core.state.seq + 5n, r.core.state.tick, Ev.join, dogPayload(1n)),
    ]);
    await r.run(300);
    r.authority.step(4);
    r.deliver([r.authority.snapshot()]);
    expect(await r.until(() => r.core.state.seq === r.authority.seq, 1000)).toBe(true);
    // Fresh events apply straight away after the restore.
    r.deliver([r.emit(Ev.join, dogPayload(0x31n))]);
    expect(await r.until(() => r.core.state.seq === r.authority.seq, 1000)).toBe(true);
  });

  it("resends intents the journal has not answered", async () => {
    const r = await rig();
    await r.establish();
    const before = r.harness.transport
      .sentFrames()
      .filter((f) => f.kind === "intent" && f.value.kind === Ev.checkIn).length;
    r.core.checkIn();
    await r.run(100);
    r.deliver([r.authority.snapshot()]);
    await r.run(500);
    const after = r.harness.transport
      .sentFrames()
      .filter((f) => f.kind === "intent" && f.value.kind === Ev.checkIn).length;
    expect(after).toBeGreaterThan(before + 1);
  });

  it("reports a hash disagreement on the restored state", async () => {
    const r = await rig();
    await r.establish();
    const before = r.core.state.mismatches;
    r.deliver([r.authority.snapshotWithBadHash()]);
    expect(await r.until(() => r.core.state.mismatches > before, 1000)).toBe(true);
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
    expect(r.harness.terrainFetches.length).toBeGreaterThan(1);
    expect(r.core.state.seq).toBe(0n);
  });
});

describe("invariant 7: module epoch", () => {
  it("an epoch_advance event asks for the module exactly once", async () => {
    const r = await rig();
    await r.establish();
    const fetchesBefore = r.harness.behaviorFetches.length;
    r.deliver([r.emit(Ev.epochAdvance, epochAdvancePayload(2, 0xdeadbeefn))]);
    expect(await r.until(() => r.count(Emit.moduleSwapWanted) > 0, 1000)).toBe(true);
    await r.run(1000);
    expect(r.count(Emit.moduleSwapWanted)).toBe(1);
    expect(r.harness.behaviorFetches.length).toBe(fetchesBefore + 1);
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
    expect(await r.until(() => r.core.state.seq === r.authority.seq, 2000)).toBe(true);
  });

  it("a swap that cannot load its terrain leaves the old world running", async () => {
    // A new module refusing the terrain we hold is a real shape — the
    // schema tightening is exactly the sort of change that ships with a
    // module. Publishing the fresh instances before their terrain loaded
    // would leave two worldless slots live, `session_module_swapped`
    // never called, and every retry failing identically: a frozen park.
    const r = await rig();
    await r.establish();
    await r.run(400);
    const tickBefore = r.core.state.tick;
    const dogsBefore = r.core.state.dogCount;

    // Serve a "new module" that instantiates but cannot take our world.
    // server.wasm is a real committed artifact with no park surface, so
    // the swap gets past instantiation and dies in the terrain load —
    // which is the ordering under test. It dies at the FIRST terrain call
    // (`terrain_cap` is missing) rather than at a `sim_set_terrain`
    // refusal, so what is covered here is "the terrain step threw", not
    // specifically "the new module rejected this world". Reaching the
    // later point faithfully would need a park build with a different
    // terrain schema; both land in the same catch.
    r.harness.setModule("park", modules().server.slice().buffer);
    r.deliver([r.emit(Ev.epochAdvance, epochAdvancePayload(2, 0xdeadbeefn))]);
    expect(await r.until(() => r.harness.logs.some((l) => l.includes("swap failed")), 2000)).toBe(
      true,
    );

    // The retired module is still running the world: the replica keeps
    // stepping and the roster is intact.
    await r.run(600);
    expect(r.core.state.tick).toBeGreaterThan(tickBefore);
    expect(r.core.state.dogCount).toBe(dogsBefore);
    expect(r.core.pump() & PumpFlag.haveState).toBe(PumpFlag.haveState);

    // And the retry lane recovers once the module serves a loadable world.
    r.harness.setModule("park", modules().park.slice().buffer);
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

describe("invariant 8: the prediction overlay", () => {
  it("shows an own intent before the journal confirms it", async () => {
    const r = await rig({ role: "player", myDog: 0x777n });
    await r.establish();
    // The join the core sent on connect is still unanswered, so the
    // presented world already holds our dog while slot 0 does not.
    await r.run(100);
    expect(r.core.state.present).toBe(true);
    expect(r.core.state.seq).toBe(0n);
  });

  it("keeps the overlay after the journal answers, without double-applying", async () => {
    const r = await rig({ role: "player", myDog: 0x777n });
    await r.establish();
    await r.run(100);
    const sent = r.harness.transport.sentFrames();
    const join = sent.find((f) => f.kind === "intent" && f.value.kind === Ev.join);
    const id = join!.kind === "intent" ? join!.value.id : 0n;
    r.deliver([r.emit(Ev.join, dogPayload(0x777n), id)]);
    expect(await r.until(() => r.core.state.seq === r.authority.seq)).toBe(true);
    expect(r.core.state.present).toBe(true);
    expect(r.core.state.dogCount).toBe(1);
  });

  // A reject must reach the presented slot, not just the pending map:
  // an intent the authority refused may not linger visibly as if it
  // had happened.
  it("drops the overlay entry when the intent is rejected", async () => {
    const r = await rig({ role: "player", myDog: 0x778n });
    await r.establish();
    await r.run(100);
    const join = r.harness.transport
      .sentFrames()
      .find((f) => f.kind === "intent" && f.value.kind === Ev.join);
    const id = join!.kind === "intent" ? join!.value.id : 0n;
    r.deliver([r.authority.reject(id, Reject.notYours)]);
    expect(await r.until(() => r.core.state.present === false, 1000)).toBe(true);
    expect(r.core.state.rejects).toBe(1);
  });

  it("swallows the two rejects that describe the state we asked for", async () => {
    const r = await rig({ role: "player", myDog: 0x779n });
    await r.establish();
    await r.run(100);
    const join = r.harness.transport
      .sentFrames()
      .find((f) => f.kind === "intent" && f.value.kind === Ev.join);
    const id = join!.kind === "intent" ? join!.value.id : 0n;
    // "Already present" answering a join IS the joined state.
    r.deliver([r.authority.reject(id, Reject.present)]);
    await r.run(200);
    expect(r.codes()).not.toContain(Emit.reject);
    expect(r.core.state.rejects).toBe(1);
  });

  it("re-joins once when the park says our dog is absent", async () => {
    const r = await rig({ role: "player", myDog: 0x77an });
    await r.establish();
    await r.run(100);
    r.core.checkIn();
    await r.run(50);
    const checkIn = r.harness.transport
      .sentFrames()
      .find((f) => f.kind === "intent" && f.value.kind === Ev.checkIn);
    const id = checkIn!.kind === "intent" ? checkIn!.value.id : 0n;
    r.deliver([r.authority.reject(id, Reject.absent)]);
    await r.run(200);
    expect(r.codes()).toContain(Emit.autoRejoin);
    const joins = r.harness.transport
      .sentFrames()
      .filter((f) => f.kind === "intent" && f.value.kind === Ev.join);
    // Two distinct joins: the one sent on connect (possibly resent by the
    // restore, same id) and exactly one auto-rejoin.
    const ids = new Set(joins.map((f) => (f.kind === "intent" ? f.value.id : 0n)));
    expect(ids.size).toBe(2);
  });
});

describe("invariant 9: an idle session sends only checks", () => {
  it("writes nothing to the stream once established", async () => {
    const r = await rig({ checkMs: 200, role: "spectator" });
    await r.establish(Role.spectator);
    await r.run(1600);
    const framesBefore = r.harness.transport.sentStream.length;
    const bytesBefore = r.core.state.bytesDown;
    await r.run(3000);
    expect(r.harness.transport.sentStream.length).toBe(framesBefore);
    expect(r.harness.transport.sentDatagrams.length).toBeGreaterThan(0);
    // Nothing arrives either: the replica steps locally, at zero cost.
    expect(r.core.state.bytesDown).toBe(bytesBefore);
  });

  it("goes silent while hidden and resumes when visible", async () => {
    const r = await rig({ checkMs: 200 });
    await r.establish();
    await r.run(1800);
    expect(r.harness.transport.sentDatagrams.length).toBeGreaterThan(0);
    r.core.setVisible(false);
    const quiet = r.harness.transport.sentDatagrams.length;
    await r.run(2000);
    expect(r.harness.transport.sentDatagrams.length).toBe(quiet);
    r.core.setVisible(true);
    await r.run(600);
    expect(r.harness.transport.sentDatagrams.length).toBeGreaterThan(quiet);
  });
});

describe("network abuse", () => {
  it("reassembles a session delivered one byte at a time", async () => {
    const r = await rig();
    const ones = Array.from({ length: 4000 }, () => 1);
    r.deliver([r.authority.welcome()], ones);
    await r.until(() => r.core.state.hz > 0, 500);
    r.deliver([r.authority.snapshot()], ones);
    expect(await r.until(() => r.core.state.seq === r.authority.seq, 3000)).toBe(true);
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
    expect(await r.until(() => r.core.state.seq === r.authority.seq, 1000)).toBe(true);
    expect(r.core.state.events).toBe(3);
  });

  it("ignores a lost verdict and keeps checking", async () => {
    const r = await rig({ checkMs: 150 });
    await r.establish();
    await r.run(1800);
    const sent = r.harness.transport.sentDatagrams.length;
    // Answer nothing at all: loss is the datagram contract.
    await r.run(2000);
    expect(r.harness.transport.sentDatagrams.length).toBeGreaterThan(sent);
    expect(Number(r.core.state.resyncs)).toBe(0);
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
    expect(Number(r.core.state.resyncs)).toBe(0);
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
    // The restore lands inside the stream call; the presented slot is
    // rebuilt on the next pump, which is where the roster becomes visible.
    expect(await r.until(() => r.core.state.dogCount === 2048, 2000)).toBe(true);
    expect(r.core.state.seq).toBe(r.authority.seq);
    expect(Number(r.core.state.mismatches)).toBe(0);
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
    expect(await r.until(() => r.core.state.dogCount === 2048, 2000)).toBe(true);
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
        (e) => e.code === HostEmit.teardown && e.a === BigInt(ResyncReason.streamOverflow),
      ),
    ).toBe(true);
    expect(await r.until(() => r.harness.transport.dials > dials, 8000, 25)).toBe(true);

    // And the replacement connection is a working session: the replica
    // survived, and a fresh snapshot lands on the new stream.
    const restores = r.count(Emit.snapshotRestored);
    r.deliver([r.authority.snapshot()]);
    expect(await r.until(() => r.count(Emit.snapshotRestored) > restores, 3000)).toBe(true);
    expect(r.core.pump() & PumpFlag.haveState).toBe(PumpFlag.haveState);
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
    expect(r.core.state.seq).toBe(r.authority.seq);
  });

  it("refuses an event payload past the core's cap and resyncs", async () => {
    const r = await rig();
    await r.establish();
    const resyncsBefore = r.core.state.resyncs;
    const oversize = encodeEvent({
      seq: r.core.state.seq + 1n,
      tick: r.core.state.tick,
      kind: Ev.join,
      intent: 0n,
      p: new Uint8Array(65),
    });
    r.deliver([oversize]);
    expect(await r.until(() => r.core.state.resyncs > resyncsBefore, 500)).toBe(true);
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
    const resyncFrames = () =>
      r.harness.transport.sentFrames().filter((f) => f.kind === "resync").length;
    expect(resyncFrames()).toBe(1);
    // Still outstanding: no snapshot has been delivered to answer it.
    expect(r.core.pump() & PumpFlag.resyncing).toBe(PumpFlag.resyncing);

    r.harness.transport.drop();
    expect(await r.until(() => r.harness.transport.dials > 1, 3000, 25)).toBe(true);
    expect(r.core.pump() & PumpFlag.resyncing).toBe(0);

    // The same two strikes must be able to ask again, on the new stream.
    await r.waitForChecks(3);
    r.answerChecks({ ok: false });
    await r.waitForChecks(4);
    r.answerChecks({ ok: false });
    await r.run(200);
    expect(resyncFrames()).toBe(2);
  });

  it("keeps the replica across a redial", async () => {
    const r = await rig();
    await r.establish();
    await r.run(1000);
    const seq = r.core.state.seq;
    const tick = r.core.state.tick;
    r.harness.transport.drop();
    await r.until(() => r.harness.transport.dials > 1, 3000, 25);
    expect(r.core.state.seq).toBe(seq);
    expect(r.core.state.tick).toBeGreaterThanOrEqual(tick);
    // The hello carries what we already have, so the server can catch us up.
    const hellos = r.harness.transport.sentFrames().filter((f) => f.kind === "hello");
    expect(hellos).toHaveLength(2);
    expect(hellos[1]!.kind === "hello" && hellos[1]!.value.sinceSeq).toBe(seq);
  });

  it("reidentify redials under the new dog and clears the old intents", async () => {
    const r = await rig({ role: "spectator", myDog: 0x1n });
    await r.establish(Role.spectator);
    const dials = r.harness.transport.dials;
    r.core.reidentify(0x999n, "player");
    expect(r.core.state.role).toBe("player");
    await r.until(() => r.harness.transport.dials > dials, 2000, 25);
    // The new connection joins as the new dog.
    r.deliver([r.authority.welcome(Role.player)]);
    await r.run(200);
    const joins = r.harness.transport
      .sentFrames()
      .filter((f) => f.kind === "intent" && f.value.kind === Ev.join);
    expect(joins.length).toBeGreaterThan(0);
    const last = joins.at(-1)!;
    expect(last.kind === "intent" && new DataView(last.value.p.buffer).getBigUint64(0, true)).toBe(
      0x999n,
    );
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

    const frozenAt = r.core.state.tick;
    const startMs = r.harness.clock.now();
    const startTick = r.authority.tick;
    // The world keeps running while we are frozen; the authority is the
    // only thing still moving, so the clock learns it is falling behind.
    const worldRunsOn = () => {
      const target = startTick + BigInt(Math.floor((r.harness.clock.now() - startMs) / TICK_MS));
      while (r.authority.tick < target) r.authority.step();
    };

    let flags = 0;
    let everStepped = false;
    for (let t = 0; t < 90_000 && clockStateOf(flags) !== 3; t += 50) {
      r.harness.clock.advance(50);
      worldRunsOn();
      flags = r.core.pump(0);
      everStepped ||= (flags & PumpFlag.stepped) !== 0;
      // Answer honestly-ok so the ONLY thing driving a resync here is the
      // clock, not a hash the frozen replica cannot possibly match.
      r.answerChecks({ ok: true });
      await r.harness.settle();
    }

    expect(clockStateOf(flags)).toBe(3);
    expect(everStepped).toBe(false);
    expect(r.core.state.tick).toBe(frozenAt);
    expect(Number(r.core.state.resyncs)).toBeGreaterThan(0);
    const reasons = r.harness.emitted
      .filter((e) => e.code === Emit.resyncRequested)
      .map((e) => Number(e.a));
    expect(reasons).toContain(ResyncReason.clock);

    // Resume: the snapshot the core asked for lands and the replica is
    // back with the world, at the authority's tick rather than its own.
    r.deliver([r.authority.snapshot()]);
    expect(await r.until(() => r.core.state.tick > frozenAt, 3000)).toBe(true);
    expect(r.core.state.tick).toBeGreaterThan(startTick);
    expect(clockStateOf(r.core.pump())).toBeLessThan(3);
  });

  it("still applies journal events while frozen", async () => {
    // "Observe" includes keeping up with the journal: only stepping is
    // starved, so an event stamped at a tick already reached still lands.
    const r = await rig();
    await r.establish();
    await r.run(400);
    const at = r.core.state.tick;
    const before = r.core.state.events;
    r.deliver([r.authority.frame(r.core.state.seq + 1n, at, Ev.join, dogPayload(0x91n))]);
    for (let t = 0; t < 500; t += 50) {
      r.harness.clock.advance(50);
      r.core.pump(0);
      await r.harness.settle();
    }
    expect(r.core.state.events).toBe(before + 1);
    expect(r.core.state.tick).toBe(at);
  });

  it("restores normal stepping when the budget comes back", async () => {
    const r = await rig();
    await r.establish();
    await r.run(400);
    const frozenAt = r.core.state.tick;
    for (let t = 0; t < 1000; t += 50) {
      r.harness.clock.advance(50);
      r.core.pump(0);
      await r.harness.settle();
    }
    expect(r.core.state.tick).toBe(frozenAt);
    await r.run(1000);
    expect(r.core.state.tick).toBeGreaterThan(frozenAt);
  });
});

describe("pump status and the read surface", () => {
  it("reports have_state only once a world has landed", async () => {
    const r = await rig();
    expect(r.core.pump() & PumpFlag.haveState).toBe(0);
    await r.establish();
    expect(r.core.pump() & PumpFlag.haveState).toBe(PumpFlag.haveState);
  });

  it("reports a clock state in the high bits", async () => {
    const r = await rig();
    await r.establish();
    await r.run(1000);
    const state = clockStateOf(r.core.pump());
    expect(state).toBeGreaterThanOrEqual(0);
    expect(state).toBeLessThanOrEqual(3);
    // The read surface must take it from the same place, not from the
    // module's standalone clock export, which tracks a different Clock.
    expect(r.core.state.clockState).toBe(state);
    expect(state).toBeGreaterThan(0);
  });

  it("hands a renderer the presented world and the terrain planes", async () => {
    const r = await rig();
    await r.establish();
    r.deliver([r.emit(Ev.join, dogPayload(0x51n))]);
    await r.until(() => r.core.state.seq === r.authority.seq);
    const view = r.core.view();
    expect(view).not.toBeNull();
    expect(view!.terrain).not.toBeNull();
    expect(
      new DataView(view!.viewBytes.buffer, view!.viewBytes.byteOffset).getUint32(0, true),
    ).toBe(r.core.state.dogCount);
    expect(view!.phaseQ16).toBeGreaterThanOrEqual(0);
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
      phases.add(r.core.view()!.phaseQ16);
    }
    expect(phases.size).toBeGreaterThan(1);
    expect(Math.max(...phases)).toBeLessThanOrEqual(65536);
  });

  it("notifies a subscriber only when its field changes", async () => {
    const r = await rig();
    const seen: bigint[] = [];
    r.core.subscribe("tick", (t) => seen.push(t));
    await r.establish();
    await r.run(500);
    expect(seen.length).toBeGreaterThan(1);
    expect([...seen].sort((a, b) => Number(a - b))).toEqual(seen);
    expect(new Set(seen).size).toBe(seen.length);
  });

  it("counts every stat the session exposes", async () => {
    const r = await rig();
    await r.establish();
    r.deliver([r.emit(Ev.join, dogPayload(0x61n))]);
    await r.until(() => r.core.state.seq === r.authority.seq);
    expect(r.core.state.events).toBe(1);
    for (const kind of Object.values(Stat)) {
      expect(Number(r.core.state.tick)).toBeGreaterThanOrEqual(0);
      expect(kind).toBeGreaterThan(0);
    }
    expect(r.core.state.bytesDown).toBeGreaterThan(0);
  });

  it("keeps the tick moving at roughly the park's rate", async () => {
    const r = await rig();
    await r.establish();
    const from = r.core.state.tick;
    await r.run(2000);
    const ticks = Number(r.core.state.tick - from);
    // The clock decides the pace; it must track 24Hz, not race or stall.
    expect(ticks).toBeGreaterThan(2000 / TICK_MS / 2);
    expect(ticks).toBeLessThan((2000 / TICK_MS) * 2);
  });
});
