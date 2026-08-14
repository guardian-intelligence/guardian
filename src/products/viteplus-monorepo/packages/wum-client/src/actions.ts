/**
 * The intent kinds a WUM client can send, named for dashboards and panes.
 * The numbers are the park's event kinds — an action's kind rides its
 * spans as this number, so a NEW action needs no telemetry change, only a
 * row here; the conformance suite fails when the client sends a kind this
 * table cannot name.
 */
export const ActionKind = {
  1: "join",
  3: "check_in",
  4: "move_to",
  8: "boost",
} as const;

export type ActionKindCode = keyof typeof ActionKind;

/** The dashboard name for an action kind, or null for one the table cannot name. */
export function actionName(kind: number): string | null {
  return Object.hasOwn(ActionKind, kind) ? ActionKind[kind as ActionKindCode] : null;
}
