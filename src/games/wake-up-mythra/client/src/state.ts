import type { HostState } from "@guardian/chunkies";
import type { Hud } from "./projections/hud.ts";

export type RoleName = "player" | "spectator";

/**
 * The game's reactive lane, shaped for an external-store binding
 * (`getState`/`subscribe`): low-frequency facts only. Frame data — dog
 * positions, phase — moves exclusively through `WumGame.frame` pull.
 */
export type GameState = {
  readonly connection: HostState;
  /** The viewer's HUD record; last known values survive a frame the module cannot answer. */
  readonly hud: Hud | null;
  readonly worldTick: bigint;
  /** The last verdict's answer, or null before one arrives. */
  readonly worldHashOk: boolean | null;
};
