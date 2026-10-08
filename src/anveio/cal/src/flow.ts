import { useCallback, useEffect, useRef, useState } from "react";

import { useGoogleSignIn, type AuthState, type Failure } from "./auth/google.ts";
import { LINES, STAGE_LINE, type Stage } from "./content.ts";
import { applyStack, type Card, type StackOp } from "./stack.ts";

// Samantha's run through the page. A forced stage (from ?frame=) freezes
// the flow on that frame with Rumi's line already sent.

// How long Rumi "types" before a line lands, like iMessage's typing
// indicator: a beat, plus a little for longer lines.
const typingMs = (line: string) => Math.min(900 + line.length * 9, 2000);

const FAILURE_LINE = {
  cancelled: LINES.signinCancelled,
  blocked: LINES.signinBlocked,
  invalid: LINES.signinInvalid,
} as const satisfies Record<Failure, string>;

export type CardKind = "draft1" | "fri" | "draft2" | "thu";

const NEXT_ON_LEAVE: Partial<Record<Stage, Stage>> = {
  arrive: "signin",
  booked: "return",
  return: "updating",
  rebooked: "return",
  novoice: "updating",
};

function cardFor(stage: Stage, heard: boolean): CardKind | null {
  switch (stage) {
    case "listening":
      return heard ? "draft1" : null;
    case "thinking":
      return "draft1";
    case "booked":
      return "fri";
    case "updating":
      return heard ? "draft2" : null;
    case "thinking2":
      return "draft2";
    case "rebooked":
      return "thu";
    default:
      return null;
  }
}

export function useFlow(forced: Stage | null) {
  const [liveStage, setLiveStage] = useState<Stage>("arrive");
  const [heard, setHeard] = useState(false);
  const [typing, setTyping] = useState(false);
  const [liveStack, setLiveStack] = useState<readonly Card[]>(["message"]);
  const timers = useRef<number[]>([]);

  const google = useGoogleSignIn();

  const stage = forced ?? liveStage;
  // ?frame=signing freezes the card waiting on Google
  const auth: AuthState = forced
    ? { status: forced === "signing" ? "waiting" : "signedOut" }
    : google.state;
  const signingIn = auth.status === "waiting" || auth.status === "verifying";
  // While Google's window is open Rumi keeps her last line, so a retry
  // doesn't make her repeat herself.
  const said = useRef<string>(STAGE_LINE[stage]);
  if (!signingIn)
    said.current =
      stage === "signin" && auth.status === "failed"
        ? FAILURE_LINE[auth.failure]
        : STAGE_LINE[stage];
  const line = said.current;
  const stack: readonly Card[] = forced
    ? forced === "signin" || forced === "signing"
      ? ["signin", "message"]
      : ["message"]
    : liveStack;

  // Rumi's hand on the stack (see stack.ts); frozen frames ignore her.
  const rumi = useCallback(
    (op: StackOp) => {
      if (!forced) setLiveStack((s) => applyStack(s, op));
    },
    [forced],
  );

  // Each new line: Rumi types for a moment, then the whole message lands at
  // once. She counts as speaking while she types.
  useEffect(() => {
    if (forced) return;
    setTyping(true);
    const t = window.setTimeout(() => setTyping(false), typingMs(line));
    return () => window.clearTimeout(t);
  }, [line, forced]);

  const clear = () => {
    timers.current.forEach((t) => window.clearTimeout(t));
    timers.current = [];
  };
  const later = (fn: () => void, ms: number) => timers.current.push(window.setTimeout(fn, ms));

  const go = useCallback(
    (next: Stage) => {
      if (forced) return;
      clear();
      setLiveStage(next);
      setHeard(false);
      if (next === "listening" || next === "updating") later(() => setHeard(true), 1700);
      if (next === "thinking") later(() => go("booked"), 2200);
      if (next === "thinking2") later(() => go("rebooked"), 2400);
    },
    [forced],
  );

  useEffect(() => clear, []);

  // Google said yes: Rumi takes the Sign in card away and starts listening.
  const signedIn = auth.status === "signedIn";
  useEffect(() => {
    if (!signedIn || forced || liveStage !== "signin") return;
    rumi({ op: "remove", card: "signin" });
    go("listening");
  }, [signedIn, forced, liveStage, rumi, go]);

  const recording = stage === "listening" || stage === "updating";
  const thinking = stage === "thinking" || stage === "thinking2";
  const speaking = !forced && typing;

  return {
    stage,
    host: stage === "host",
    card: cardFor(stage, forced ? true : heard),
    line,
    typing: speaking,
    speaking,
    orbMode: speaking
      ? "speaking"
      : recording
        ? "listening"
        : thinking || signingIn
          ? "thinking"
          : "idle",
    auth,
    control: stage === "host" ? "host" : recording ? "recording" : thinking ? "thinking" : "idle",
    stack,
    rumi,
    // A signed-out guest leaving a message: Rumi puts "Sign in" on top first.
    leave: () => {
      const next = NEXT_ON_LEAVE[stage];
      if (next === "signin" && signedIn) go("listening");
      else if (next === "signin") {
        rumi({ op: "add", card: "signin" });
        go("signin");
      } else if (next) go(next);
    },
    // Opens Google's window, so it must run straight from the tap.
    signIn: () => {
      if (!forced) google.open();
    },
    // The guest swiped the front card away; swiping Sign in also abandons
    // any sign-in still in progress.
    dismiss: (card: Card) => {
      rumi({ op: "remove", card });
      if (card === "signin") google.cancel();
      if (card === "signin" && stage === "signin") go("arrive");
    },
    send: () => go(stage === "updating" ? "thinking2" : "thinking"),
    cancel: () => go(stage === "updating" ? "return" : "arrive"),
  } as const;
}
