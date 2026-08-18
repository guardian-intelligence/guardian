/**
 * WUM's extension exports on the session module — the intent verbs the
 * framework host does not know. Reached through
 * `ReplicaHost.extension<WumIntents>()`; each returns the intent id, or
 * 0n when the module refused to mint one.
 */
export interface WumIntents {
  intent_join(nowMs: bigint): bigint;
  intent_check_in(nowMs: bigint): bigint;
  intent_move_to(node: number, nowMs: bigint): bigint;
  intent_boost(on: number, nowMs: bigint): bigint;
}
