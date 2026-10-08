// The card stack beside Rumi. She arranges it; the guest only acts on the
// front card or swipes it away. Each op is shaped like a tool call so the
// agent can drive it directly: add a card (on top unless `at` says
// otherwise), remove one, or move one to a new position (0 = front).

export const CARDS = ["message", "signin"] as const;
export type Card = (typeof CARDS)[number];

export type StackOp =
  | { readonly op: "add"; readonly card: Card; readonly at?: number }
  | { readonly op: "remove"; readonly card: Card }
  | { readonly op: "move"; readonly card: Card; readonly to: number };

// Cards the guest may swipe away. "Leave a message" is the one control that
// always stays; swiping it only rubber-bands.
export const DISMISSABLE = { message: false, signin: true } as const satisfies Record<
  Card,
  boolean
>;

const clampIndex = (i: number, len: number) => Math.max(0, Math.min(Math.trunc(i), len));

export function applyStack(stack: readonly Card[], op: StackOp): readonly Card[] {
  if (op.op === "move" && !stack.includes(op.card)) return stack;
  const rest = stack.filter((c) => c !== op.card);
  switch (op.op) {
    case "add":
    case "move": {
      const at = clampIndex(op.op === "add" ? (op.at ?? 0) : op.to, rest.length);
      return [...rest.slice(0, at), op.card, ...rest.slice(at)];
    }
    case "remove":
      return rest;
  }
}
