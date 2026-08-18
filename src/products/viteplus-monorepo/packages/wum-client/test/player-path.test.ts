// The player path: a session that owns a dog, sends intents, and has to
// reconcile what it asked for against what the journal answers. Spectator
// probes never exercise any of it — nothing to ack, no join to race — so
// these invariants need their own suite.

import { describe, expect, it } from "vitest";
import { Emit, ResyncReason, type PumpStatus } from "@guardian/chunkies";
import {
  bringTheDogIn,
  DEFAULT_RTT_MS,
  dogPayload,
  echoPayload,
  Ev,
  Reject,
  Role,
  type ClientFrame,
} from "@guardian/chunkies-testkit";
import { GLIDE_MAX_CELLS, GLIDE_MAX_CELLS_PER_SEC } from "../src/projections/view.ts";
import { wumRig, type WumRig } from "./rig.ts";

/** Ring entries land one per second, so this is the window after a restore with none. */
const RING_CADENCE_TICKS = 24;

function joinFrames(r: WumRig) {
  return r.harness.transport
    .sentFrames()
    .filter((f) => f.kind === "intent" && f.value.kind === Ev.join);
}

function idOf(frame: ClientFrame | undefined): bigint {
  return frame?.kind === "intent" ? frame.value.intent : 0n;
}

function intentFrames(r: WumRig, kind: number) {
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
    const r = await wumRig({ role: "player" });
    await r.establish();
    const restoredAt = r.state.tick;
    const resyncsBefore = Number(r.state.resyncs);
    const rollbacksBefore = r.state.rollbacks;

    // Step a few ticks — but stay inside one ring cadence, so the only
    // state old enough to roll back to is the snapshot itself.
    await r.run(200);
    expect(Number(r.state.tick - restoredAt)).toBeLessThan(RING_CADENCE_TICKS);
    expect(r.state.tick).toBeGreaterThan(restoredAt);

    // An event stamped at the restored tick: reachable, by definition.
    r.deliver([r.authority.frame(r.state.seq + 1n, restoredAt, Ev.join, dogPayload(0xa11n))]);
    expect(await r.until(() => r.state.events > 0, 1000)).toBe(true);
    expect(r.state.rollbacks).toBeGreaterThan(rollbacksBefore);
    expect(Number(r.state.resyncs)).toBe(resyncsBefore);
  });

  it("does not answer one snapshot by asking for the next", async () => {
    // The shape that turns a single late event into a loop: each resync
    // lands a snapshot, and a snapshot that cannot be rolled back to
    // leaves the next late event with nothing to repair from either.
    const r = await wumRig({ role: "player" });
    await r.establish();
    const resyncsBefore = Number(r.state.resyncs);

    for (let i = 0; i < 3; i++) {
      await r.run(150);
      r.deliver([
        r.authority.frame(
          r.state.seq + 1n,
          r.state.tick - 2n,
          Ev.join,
          dogPayload(BigInt(0xb00 + i)),
        ),
      ]);
      await r.run(150);
    }
    expect(Number(r.state.resyncs)).toBe(resyncsBefore);
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
    const r = await wumRig({ role: "player" });
    await r.establish();
    await r.run(300);
    expect(joinFrames(r)).toHaveLength(1);
  });

  it("keeps one join across a redial rather than minting another", async () => {
    // A reconnect resends what the park has not answered; it does not ask
    // for a second dog. Two joins under two identities are two arrivals as
    // far as the authority is concerned, and only one of them can succeed.
    const r = await wumRig({ role: "player", myDog: 0x5152n });
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
    const r = await wumRig({ role: "player", myDog: 0x5150n });
    await r.establish();
    await r.run(200);
    const join = joinFrames(r)[0];
    const id = join!.kind === "intent" ? join!.value.intent : 0n;

    r.deliver([r.authority.reject(id, Reject.present)]);
    r.deliver([r.authority.reject(id, Reject.present)]);
    await r.run(200);

    const rejects = r.harness.emitted.filter((e) => e.code === Emit.reject);
    expect(rejects).toHaveLength(0);
    expect(r.harness.logs.filter((l) => /already/i.test(l))).toHaveLength(0);
  });
});

describe("intents that need a dog in the park wait for one", () => {
  it("holds a boost until the join it depends on is acknowledged", async () => {
    // A boost describes a dog that is already inside. Sent while the join
    // is still unanswered it can only be refused, and the refusal costs a
    // round trip and an auto-rejoin to undo.
    const r = await wumRig({ role: "player", myDog: 0x6001n });
    await r.establish();
    await r.run(100);
    expect(r.state.seq).toBe(0n);

    r.game.setBoost(true);
    await r.run(200);
    expect(intentFrames(r, Ev.boostSet)).toHaveLength(0);

    // Once the journal confirms the dog, the held intent may go.
    const join = joinFrames(r)[0];
    const id = join!.kind === "intent" ? join!.value.intent : 0n;
    r.deliver([r.emit(Ev.join, dogPayload(0x6001n), id)]);
    expect(await r.until(() => r.state.seq === r.authority.seq, 2000)).toBe(true);
    await r.run(200);
    expect(intentFrames(r, Ev.boostSet)).toHaveLength(1);
  });

  it("never earns an absent refusal on a fresh connection", async () => {
    // Nothing the client sends before its dog is in the park can succeed,
    // so nothing should be sent that needs it.
    const r = await wumRig({ role: "player", myDog: 0x6002n });
    await r.establish();
    r.game.setBoost(true);
    r.game.checkIn();
    r.moveTo(5);
    await r.run(300);

    const needsPresence = [Ev.boostSet, Ev.checkIn, Ev.moveTo].flatMap((k) => intentFrames(r, k));
    expect(needsPresence).toHaveLength(0);
  });
});

describe("the world is corrected under the screen", () => {
  // What the journal holds is what the renderer draws. But the journal
  // gets corrected — a snapshot restores it, a repair rewinds and replays
  // it — and a correction moves dogs without time passing, which is the
  // one thing a renderer cannot interpolate through.
  //
  // The host absorbs that at the view: it keeps showing each dog where it
  // was and closes the gap over `GLIDE_MS`. Everything here judges what a
  // viewer actually reads, `WumGame.frame()`, with no special casing — and it
  // judges the whole roster, because a correction moves the entire park
  // and one that jerks the bystanders around a smoothly walking player
  // dog is the same defect from one seat over.

  /** One cell in the Q16.16 coordinates `sim_view` reports. */
  const CELL = 65536;
  /** One way of the rig's round trip: how long anything is in flight. */
  const ONE_WAY_MS = DEFAULT_RTT_MS / 2;
  /** One tick at the fixture park's 24Hz, and the frame these cases sample on. */
  const TICK_MS = 1000 / 24;
  const FRAME_MS = 8;

  type Snap = {
    tick: bigint;
    dogs: Map<bigint, { x: number; y: number }>;
    /** Did the pump behind this frame correct the world, and what did it leave owed? */
    corrected: boolean;
    debt: number;
  };

  function ownDog(r: WumRig, dog: bigint) {
    const view = r.frame();
    if (!view) return null;
    const dv = new DataView(view.viewBytes.buffer, view.viewBytes.byteOffset);
    const n = dv.getUint32(0, true);
    for (let i = 0; i < n; i++) {
      const at = 4 + i * 20;
      if (dv.getBigUint64(at, true) !== dog) continue;
      return { tick: view.tick, x: dv.getInt32(at + 8, true), y: dv.getInt32(at + 12, true) };
    }
    return null;
  }

  /** Every dog in the world as the viewer reads it, glide included. */
  function roster(r: WumRig, status: PumpStatus | null = null): Snap | null {
    const view = r.frame();
    if (!view) return null;
    const dv = new DataView(view.viewBytes.buffer, view.viewBytes.byteOffset);
    const n = dv.getUint32(0, true);
    const dogs = new Map<bigint, { x: number; y: number }>();
    for (let i = 0; i < n; i++) {
      const at = 4 + i * 20;
      dogs.set(dv.getBigUint64(at, true), {
        x: dv.getInt32(at + 8, true),
        y: dv.getInt32(at + 12, true),
      });
    }
    return {
      tick: view.tick,
      dogs,
      corrected: status?.corrected ?? false,
      debt: r.game.glideDebtCells(),
    };
  }

  /** Walkable cells, for sending dogs somewhere. */
  function walkableNodes(r: WumRig): number[] {
    const t = r.frame()!.terrain!;
    const out: number[] = [];
    for (let i = 0; i < t.w * t.h; i++) if (t.ground[i] === 1 && t.deck[i] === 0) out.push(i);
    return out;
  }

  /** The 10-byte move_to payload: dog id, then the node. */
  function moveToPayload(dog: bigint, node: number): Uint8Array {
    const p = new Uint8Array(10);
    const dv = new DataView(p.buffer);
    dv.setBigUint64(0, dog, true);
    dv.setUint16(8, node, true);
    return p;
  }

  /** A walkable cell far from where the dog is standing. */
  function farTarget(r: WumRig, from: { x: number; y: number }) {
    const t = r.frame()!.terrain!;
    const fx = from.x / CELL;
    const fy = from.y / CELL;
    let best = -1;
    let bestD = 0;
    for (let i = 0; i < t.w * t.h; i++) {
      if (t.ground[i] !== 1 || t.deck[i] !== 0) continue;
      const d = Math.hypot((i % t.w) - fx, Math.floor(i / t.w) - fy);
      if (d > bestD) {
        bestD = d;
        best = i;
      }
    }
    return { node: best, cells: bestD };
  }

  /**
   * The authority answering what the session writes. An intent reaches the
   * park a one-way trip after it is sent, is applied to the canonical world
   * at whatever tick the park holds then, and the event announcing it flies
   * back over another. That round trip IS the response time now: nothing
   * moves our dog until the journal says so.
   */
  function parkAnswers(r: WumRig): () => void {
    let cursor = r.harness.transport.sentFrames().length;
    return () => {
      const written = r.harness.transport.sentFrames();
      for (; cursor < written.length; cursor++) {
        const frame = written[cursor]!;
        if (frame.kind !== "intent") continue;
        const intent = frame.value;
        r.harness.clock.schedule(() => {
          const event = r.authority.apply(intent.kind, echoPayload(intent), intent.intent);
          r.harness.clock.schedule(() => {
            r.deliver([event]);
          }, ONE_WAY_MS);
        }, ONE_WAY_MS);
      }
    };
  }

  /** One frame of a live session: time passes, the core pumps, the park keeps up. */
  async function frameStep(r: WumRig, answer: () => void): Promise<PumpStatus> {
    r.harness.clock.advance(FRAME_MS);
    const status = r.pump();
    while (r.authority.tick < r.state.tick) r.authority.step();
    r.answerChecks();
    answer();
    await r.harness.settle();
    return status;
  }

  /**
   * Within one tick the world does not move, so anything a viewer sees
   * move there is the PRESENTER — and the presenter is rate-limited. The
   * bound is the mechanism's own declared speed rather than a number
   * chosen here, so it cannot drift away from what the presenter is
   * actually allowed to do.
   *
   * Exact equality would be the wrong shape: a glide in progress moves a
   * dog a little between two frames of a tick BY DESIGN, and demanding
   * equality would be demanding the mechanism not work. What it must never
   * do is exceed its own rate — that is a raw correction reaching the
   * screen.
   *
   * One case is deliberately not covered, and the exemption is left
   * unwritten until something needs it. A terrain change relocates dogs
   * whose ground has gone, and a relocation past `GLIDE_MAX_CELLS` is
   * SHOWN rather than glided — a world that changed under the player is
   * not a correction to hide. A scenario with one in it wants this bound
   * lifted only for frames the pump reported corrected, never lifted
   * generally: an exemption written before there is a case to justify it
   * is a hole with a comment over it.
   */
  function expectNoSameTickJump(snaps: Snap[], what: string): void {
    const cap = (GLIDE_MAX_CELLS_PER_SEC * FRAME_MS) / 1000;
    for (let i = 1; i < snaps.length; i++) {
      const now = snaps[i]!;
      const was = snaps[i - 1]!;
      if (now.tick !== was.tick) continue;
      for (const [id, at] of now.dogs) {
        const before = was.dogs.get(id);
        // A dog the previous frame did not have is arriving, not moving.
        if (!before) continue;
        const moved = Math.hypot((at.x - before.x) / CELL, (at.y - before.y) / CELL);
        expect(
          moved,
          `${what}: dog ${id.toString(16)} moved ${moved.toFixed(4)} cells inside tick ` +
            `${now.tick} — this frame corrected: ${now.corrected}, previous frame: ` +
            `${was.corrected}, owed before ${was.debt.toFixed(4)} after ${now.debt.toFixed(4)}`,
        ).toBeLessThanOrEqual(cap);
      }
    }
  }

  /**
   * The presenter must always finish paying. Every frame carrying a debt
   * has to sit within one glide window of a correction — so the offset is
   * a transient, never a standing difference between what the world holds
   * and what the screen shows.
   *
   * This is the honest instrument for "the presenter adds no latency of
   * its own". Counting frames in which a dog does not move cannot say
   * that: the world's own pacing pauses dogs for a tick or two when they
   * re-plan a route, and a gate on stillness measures that instead —
   * loudly, and with nothing to do with the presenter.
   *
   * The window is the largest the presenter can choose for itself: the
   * biggest correction it will carry, at its slowest permitted rate.
   */
  const GLIDE_WINDOW_MAX_MS = (1.5 * GLIDE_MAX_CELLS * 1000) / GLIDE_MAX_CELLS_PER_SEC;

  function expectDebtSettles(snaps: Snap[], what: string): void {
    let sinceCorrection = Number.POSITIVE_INFINITY;
    for (const s of snaps) {
      sinceCorrection = s.corrected ? 0 : sinceCorrection + FRAME_MS;
      if (s.debt === 0) continue;
      expect(
        sinceCorrection,
        `${what}: presenter still owed ${s.debt.toFixed(4)} cells at tick ${s.tick}`,
      ).toBeLessThanOrEqual(GLIDE_WINDOW_MAX_MS);
    }
  }

  /** Brings three dogs in and sends them walking, so a correction has bystanders to move. */
  async function fillThePark(r: WumRig): Promise<void> {
    const wander = walkableNodes(r);
    for (let i = 0; i < 3; i++) {
      const id = BigInt(0x9700 + i);
      const arrival = r.authority.apply(Ev.join, dogPayload(id));
      const walkOff = r.authority.apply(Ev.moveTo, moveToPayload(id, wander[i * 211]!));
      r.deliver([arrival, walkOff]);
    }
    const filled = await r.until(() => r.state.dogCount >= 4, 2000);
    if (!filled) throw new Error("the park never filled");
  }

  it("carries every dog across a correction, and stops carrying them", async () => {
    // A correction moves the world without time passing. The host answers
    // by showing each dog where it was and closing the gap, so a viewer
    // reads continuous motion over a world that jumped.
    //
    // The second half of the invariant is what keeps the first honest. A
    // presenter that simply held the old positions forever would satisfy
    // every continuity assertion here while showing dogs that are not
    // there, so the debt has to reach zero — and once it has, the view is
    // the world again.
    const dog = 0x9007n;
    const r = await wumRig({ role: "player", myDog: dog });
    await r.establish();
    await bringTheDogIn(r, dog);
    const answer = parkAnswers(r);
    await r.run(200);
    await fillThePark(r);

    const standing = ownDog(r, dog)!;
    r.moveTo(farTarget(r, standing).node);
    let corrections = 0;
    let underWay = -1;
    const snaps: Snap[] = [];
    for (let t = 0; t < 1600; t += FRAME_MS) {
      // A snapshot mid-walk: the world it carries was taken where the park
      // stands, so it lands with every dog somewhere the screen does not
      // have them.
      if (t === 600) r.deliver([r.authority.snapshot()]);
      const status = await frameStep(r, answer);
      if (status.corrected) corrections++;
      const s = roster(r, status);
      if (!s) continue;
      snaps.push(s);
      const at = s.dogs.get(dog);
      if (underWay < 0 && at && (at.x !== standing.x || at.y !== standing.y)) {
        underWay = snaps.length - 1;
      }
    }

    // Coverage first: with no correction this case measures dogs walking,
    // which every other case already covers.
    expect(corrections, "pumps that corrected the world").toBeGreaterThan(0);
    expect(snaps.at(-1)!.dogs.size, "bystanders to be jerked around").toBeGreaterThan(3);
    // And the correction was one the screen would have shown: a flag that
    // fires over a world that did not actually move leaves the presenter
    // nothing to carry, and every measurement below then passes for the
    // wrong reason.
    expect(
      Math.max(...snaps.map((w) => w.debt)),
      "correction the presenter had to carry",
    ).toBeGreaterThan(0);
    expectNoSameTickJump(snaps, "across a correction");
    expect(underWay, "the dog never started walking").toBeGreaterThan(0);
    expectDebtSettles(snaps, "across a correction");
    expect(r.game.glideDebtCells(), "correction still on the presenter").toBe(0);
  });

  it("carries a boosted dog through a busy park", async () => {
    // The hard case for a presenter, and the one that used to hide a jump.
    // A boosted dog covers more ground per tick, so every correction it
    // meets is a larger one; clicks keep arriving while the last is still
    // in flight; dogs join and walk off mid-stride; and a snapshot lands on
    // top of all of it. If any path moves the world without saying so, this
    // is where it shows.
    const dog = 0x9009n;
    const r = await wumRig({ role: "player", myDog: dog });
    await r.establish();
    await bringTheDogIn(r, dog);
    const answer = parkAnswers(r);
    await r.run(200);
    await fillThePark(r);

    const wander = walkableNodes(r);
    const standing = ownDog(r, dog)!;
    // Two targets, alternating. A boosted dog crosses the park inside this
    // window, and a dog that has ARRIVED stands still legitimately — which
    // would read as the presenter holding it back. Keeping it walking is
    // what makes the stillness bound mean what it says.
    const here = farTarget(r, standing).node;
    const t0 = r.frame()!.terrain!;
    const there = farTarget(r, {
      x: (here % t0.w) * CELL,
      y: Math.floor(here / t0.w) * CELL,
    }).node;
    r.game.setBoost(true);
    r.moveTo(here);

    const eventsBefore = r.state.events;
    const rollbacksBefore = r.state.rollbacks;
    let corrections = 0;
    let clicks = 0;
    let joins = 0;
    let underWay = -1;
    let nextClick = 300;
    let nextJoin = 420;
    const snaps: Snap[] = [];
    for (let t = 0; t < 3000; t += FRAME_MS) {
      if (t === 1500) r.deliver([r.authority.snapshot()]);
      const status = await frameStep(r, answer);
      if (status.corrected) corrections++;
      const s = roster(r, status);
      if (s) {
        snaps.push(s);
        const at = s.dogs.get(dog);
        if (underWay < 0 && at && (at.x !== standing.x || at.y !== standing.y)) {
          underWay = snaps.length - 1;
        }
      }
      if (t >= nextClick && clicks < 6) {
        r.moveTo(clicks % 2 === 0 ? there : here);
        clicks++;
        nextClick = t + 300;
      }
      if (t >= nextJoin && joins < 6) {
        const id = BigInt(0x9800 + joins);
        const arrival = r.authority.apply(Ev.join, dogPayload(id));
        const walkOff = r.authority.apply(
          Ev.moveTo,
          moveToPayload(id, wander[(joins * 137) % wander.length]!),
        );
        r.harness.clock.schedule(() => {
          r.deliver([arrival, walkOff]);
        }, ONE_WAY_MS);
        joins++;
        nextJoin = t + 300;
      }
    }

    // Coverage, before any of it counts: the park was busy, the session
    // repaired, and the world really was corrected under the screen.
    expect(clicks, "clicks issued").toBe(6);
    expect(joins, "arrivals journalled").toBe(6);
    expect(r.state.events - eventsBefore, "journal events applied").toBeGreaterThanOrEqual(
      clicks + joins * 2,
    );
    expect(r.state.rollbacks - rollbacksBefore, "repairs performed").toBeGreaterThan(0);
    expect(corrections, "pumps that corrected the world").toBeGreaterThan(0);
    expect(snaps.at(-1)!.dogs.size, "dogs in the park").toBeGreaterThan(6);
    expect(
      Math.max(...snaps.map((w) => w.debt)),
      "correction the presenter had to carry",
    ).toBeGreaterThan(0);

    expectNoSameTickJump(snaps, "a boosted dog in a busy park");
    expect(underWay, "the dog never started walking").toBeGreaterThan(0);
    expectDebtSettles(snaps, "across a correction");
    expect(r.game.glideDebtCells(), "correction still on the presenter").toBe(0);
    expect(r.state.resyncs, "resyncs during the stretch").toBe(0);
  });

  it("starts moving within a round trip of the click", async () => {
    // What "instant" means with one world: no overhead beyond the network.
    // The click cannot move the dog before the journal has placed it —
    // that would be a second simulation — so the honest contract is that
    // nothing but the round trip and the tick it lands on stands between
    // asking and moving. This is the case that catches a presenter adding
    // latency of its own on top of the network's.
    const dog = 0x9008n;
    const r = await wumRig({ role: "player", myDog: dog });
    await r.establish();
    await bringTheDogIn(r, dog);
    const answer = parkAnswers(r);
    await r.run(200);

    const standing = ownDog(r, dog)!;
    r.moveTo(farTarget(r, standing).node);
    let movedAfterMs = -1;
    for (let t = 0; t < 2000; t += FRAME_MS) {
      await frameStep(r, answer);
      const at = ownDog(r, dog)!;
      if (at.x !== standing.x || at.y !== standing.y) {
        movedAfterMs = t + FRAME_MS;
        break;
      }
    }
    expect(movedAfterMs, "the dog never moved").toBeGreaterThan(0);
    // The trip, the tick it lands on, and the frame that draws it.
    expect(movedAfterMs, "milliseconds from the click to the first movement").toBeLessThanOrEqual(
      DEFAULT_RTT_MS + TICK_MS + 2 * FRAME_MS,
    );
  });

  it("hands a renderer the corrected world on the very next frame", async () => {
    // A snapshot lands inside a stream read, between frames. Whatever the
    // next frame shows is what the viewer sees, so the world has to have
    // caught up by then rather than one frame later.
    const r = await wumRig({ role: "player", myDog: 0x9005n });
    await r.establish();
    await bringTheDogIn(r, 0x9005n);
    await r.run(400);

    r.authority.step(12);
    const before = r.frame()!.tick;
    r.deliver([r.authority.snapshot()]);
    r.pump();

    expect(r.frame()!.tick).toBeGreaterThanOrEqual(before);
  });
});
