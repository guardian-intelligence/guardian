// The wum-client flavor of the DST rig: the same fixtures, authority and
// drive loop as the chunkies suites, wrapped around a real `WumGame`
// instead of a bare host — so the player journeys exercise the game
// layer's own doors (frame, intents, glide) end to end.

import {
  composeRig,
  rigFixtures,
  type DrivenSession,
  type Rig,
  type RigOptions,
  type RigState,
} from "@guardian/chunkies-testkit";
import { WumGame, type FrameView } from "../src/index.ts";

export type WumRig = Rig & {
  readonly game: WumGame;
  readonly state: RigState;
  /** The renderer's pull, at the virtual clock's now. */
  frame(): FrameView | null;
  /** `WumGame.moveTo` addressed by row-major node, the way the suites pick cells. */
  moveTo(node: number): bigint;
};

export async function wumRig(options: RigOptions = {}): Promise<WumRig> {
  const { harness, authority } = await rigFixtures(options);
  const game = WumGame.create(harness.ports, {
    myDog: options.myDog ?? 0x1122_3344_5566_7788n,
    role: options.role ?? "player",
    checkMs: options.checkMs ?? 5000,
    now: harness.hostOptions.now,
    schedule: harness.hostOptions.schedule,
    telemetry: harness.hostOptions.telemetry,
    sha256: harness.hostOptions.sha256,
    log: harness.hostOptions.log,
  });
  await game.boot();
  await harness.settle();

  const session: DrivenSession = {
    host: game.host,
    pump: (budgetUs) => game.pump(undefined, budgetUs),
    tick: () => game.state.worldTick,
    seq: () => game.host.diag()?.seq ?? 0n,
    hz: () => game.host.state.rateHz ?? 0,
    present: () => game.state.hud?.present ?? false,
  };

  const state = (): RigState => {
    const d = game.host.diag();
    const s = game.state;
    return {
      tick: s.worldTick,
      seq: d?.seq ?? 0n,
      hz: s.connection.rateHz ?? 0,
      role: s.connection.role,
      replicaModuleWord: s.connection.replicaModuleWord,
      clockState: d?.clockState ?? "acquiring",
      present: s.hud?.present ?? false,
      dogCount: s.hud?.dogCount ?? 0,
      events: d?.events ?? 0,
      rollbacks: d?.rollbacks ?? 0,
      resyncs: d?.resyncs ?? 0,
      checks: d?.checks ?? 0,
      mismatches: d?.mismatches ?? 0,
      rejects: d?.rejects ?? 0,
      rttMs: d?.rttMs ?? 0,
      bytesDown: harness.transport.bytesDelivered,
    };
  };

  return {
    ...composeRig(harness, authority, session, options),
    game,
    get state() {
      return state();
    },
    frame: () => game.frame(harness.clock.now()),
    moveTo: (node) => {
      const t = game.terrain();
      if (!t) return 0n;
      return game.moveTo(node % t.w, Math.floor(node / t.w));
    },
  };
}
