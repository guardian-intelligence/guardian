import { useCallback, useEffect, useRef, useState } from "react";

import { STAGE_LINE, type Stage } from "./content.ts";

// Samantha's run through the page. A forced stage (from the studio) freezes
// the flow on that frame with Rumi's line fully shown.

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
  const [typed, setTyped] = useState(0);
  const timers = useRef<number[]>([]);

  const stage = forced ?? liveStage;
  const line = STAGE_LINE[stage];

  // Stream Rumi's line a character at a time; she counts as speaking until it's out.
  useEffect(() => {
    setTyped(0);
    let n = 0;
    const iv = window.setInterval(() => {
      n += 1;
      setTyped(Math.min(n, line.length));
      if (n >= line.length) window.clearInterval(iv);
    }, 26);
    return () => window.clearInterval(iv);
  }, [line]);

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
  const restart = useCallback(() => go("arrive"), [go]);

  const recording = stage === "listening" || stage === "updating";
  const thinking = stage === "thinking" || stage === "thinking2";
  const speaking = typed < line.length;
  const shown = forced ? line.length : typed;

  return {
    stage,
    host: stage === "host",
    card: cardFor(stage, forced ? true : heard),
    line,
    shown: line.slice(0, shown),
    rest: line.slice(shown),
    speaking,
    orbMode: speaking ? "speaking" : recording ? "listening" : thinking ? "thinking" : "idle",
    control:
      stage === "host"
        ? "host"
        : stage === "signin"
          ? "signin"
          : recording
            ? "recording"
            : thinking
              ? "thinking"
              : "idle",
    leave: () => {
      const next = NEXT_ON_LEAVE[stage];
      if (next) go(next);
    },
    signIn: () => go("listening"),
    send: () => go(stage === "updating" ? "thinking2" : "thinking"),
    cancel: () => go(stage === "updating" ? "return" : "arrive"),
    restart,
  } as const;
}
