import { useEffect, useRef, useState, type PointerEvent, type ReactNode } from "react";

import { DISMISSABLE, type Card } from "../stack.ts";

// The stack beside Rumi: every card is the same size; the front one is
// live and the ones behind peek out above it, dimmer. The front card follows a sideways drag;
// let go far or fast enough to the right and it flies off and is dismissed.
// A card that can't be dismissed only gives a little and springs back.

const PEEK = 6; // px each card behind rises above the one in front
const SHOWN = 3; // cards drawn; deeper ones wait out of sight
const SLOP = 6; // px of drag before it counts as a swipe, not a tap
const FLING = 0.5; // px/ms release speed that dismisses regardless of distance
const OUT_MS = 220;

type Drag = { id: number; x0: number; t0: number; dx: number; v: number; swiped: boolean };

function Front({
  card,
  entry,
  onDismiss,
  onDrag,
  children,
}: {
  card: Card;
  entry: "arrive" | "promote" | "settled";
  onDismiss: () => void;
  /** How far toward dismissal the drag is, 0..1 (0 when not dragging). */
  onDrag: (progress: number) => void;
  children: ReactNode;
}) {
  const [enteredAs] = useState(entry); // fixed at mount so the animation runs once
  const [dx, setDx] = useState(0);
  const [phase, setPhase] = useState<"rest" | "drag" | "out">("rest");
  const drag = useRef<Drag | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const dismissable = DISMISSABLE[card];

  const fly = () => {
    onDrag(1);
    setPhase("out");
    setDx((ref.current?.offsetWidth ?? 300) + 48);
    window.setTimeout(onDismiss, OUT_MS);
  };

  const down = (e: PointerEvent<HTMLDivElement>) => {
    if (phase === "out" || e.button !== 0) return;
    drag.current = { id: e.pointerId, x0: e.clientX, t0: e.timeStamp, dx: 0, v: 0, swiped: false };
  };
  const move = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || d.id !== e.pointerId) return;
    const raw = e.clientX - d.x0;
    if (!d.swiped && Math.abs(raw) < SLOP) return;
    if (!d.swiped) {
      d.swiped = true;
      e.currentTarget.setPointerCapture(e.pointerId);
      setPhase("drag");
    }
    const dt = Math.max(e.timeStamp - d.t0, 1);
    d.v = (raw - d.dx) / dt;
    d.t0 = e.timeStamp;
    d.dx = raw;
    // rightward follows the finger; leftward, or any way on a card that
    // stays, resists like iOS rubber-banding
    const give = (x: number) => Math.sign(x) * 28 * Math.log1p(Math.abs(x) / 28);
    setDx(dismissable && raw > 0 ? raw : give(raw));
    const width = ref.current?.offsetWidth ?? 300;
    onDrag(dismissable ? Math.min(Math.max(raw / width, 0), 1) : 0);
  };
  const up = (e: PointerEvent<HTMLDivElement>) => {
    const d = drag.current;
    if (!d || d.id !== e.pointerId) return;
    drag.current = null;
    if (!d.swiped) return;
    const width = ref.current?.offsetWidth ?? 300;
    if (dismissable && (d.dx > width * 0.35 || (d.dx > 0 && d.v > FLING))) fly();
    else {
      setPhase("rest");
      setDx(0);
      onDrag(0);
    }
    // the tap that ends a swipe isn't a press of whatever is under the finger
    const swallow = (ev: Event) => {
      ev.stopPropagation();
      ev.preventDefault();
    };
    window.addEventListener("click", swallow, { capture: true, once: true });
    window.setTimeout(() => window.removeEventListener("click", swallow, { capture: true }), 0);
  };

  return (
    <div
      ref={ref}
      className={`stack-card stack-front stack-${phase} stack-${enteredAs}`}
      style={{
        transform: `translateX(${dx}px)`,
        opacity: phase === "out" ? 0 : 1,
      }}
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={up}
      onKeyDown={(e) => {
        if (dismissable && e.key === "Escape") fly();
      }}
    >
      {children}
    </div>
  );
}

type Props = {
  cards: readonly Card[];
  render: (card: Card) => ReactNode;
  onDismiss: (card: Card) => void;
};

export function Stack({ cards, render, onDismiss }: Props) {
  const front = cards[0];
  // A card that was already waiting behind comes forward; a new one lands.
  // One the guest swiped forward is already in place.
  const prev = useRef(cards);
  const swiped = useRef(false);
  const waiting = front !== undefined && prev.current.slice(1).includes(front);
  const entry = !waiting ? "arrive" : swiped.current ? "settled" : "promote";
  useEffect(() => {
    prev.current = cards;
    swiped.current = false;
  }, [cards]);
  // The cards behind ease forward as the front one is dragged away.
  const [progress, setProgress] = useState(0);
  useEffect(() => setProgress(0), [front]);
  return (
    <div className={progress > 0 ? "stack stack-dragging" : "stack"}>
      {cards
        .slice(1, SHOWN)
        .map((card, i) => {
          const depth = i + 1 - progress;
          // only the peeking strip shows until the card in front moves off it
          const clip =
            i === 0 && progress > 0 ? "none" : `inset(0 0 calc(100% - ${PEEK * depth}px) 0)`;
          return (
            <div
              key={card}
              className="stack-card stack-back"
              inert
              aria-hidden="true"
              style={{
                transform: `translateY(${-PEEK * depth}px)`,
                opacity: 1 - 0.35 * depth,
                clipPath: clip,
                zIndex: -(i + 1),
              }}
            >
              {render(card)}
            </div>
          );
        })
        .reverse()}
      {front && (
        <Front
          key={front}
          card={front}
          entry={entry}
          onDismiss={() => {
            swiped.current = true;
            onDismiss(front);
          }}
          onDrag={setProgress}
        >
          {render(front)}
        </Front>
      )}
    </div>
  );
}
