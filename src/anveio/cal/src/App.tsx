import { STAGES, type Stage } from "./content.ts";
import { Orb, type OrbMode } from "./rumi/Orb.tsx";
import { Phone } from "./ui/Phone.tsx";

// ?frame=booked opens frozen on a frame (handy for screenshots and sharing a state).
const initialFrame = (): Stage | null => {
  const f = new URLSearchParams(location.search).get("frame");
  return STAGES.find((s) => s === f) ?? null;
};

// ?lab shows Rumi alone at 640px (or &size=), unmuted, in &mode= (default
// idle): a high-resolution view of her simulation to judge it by.
const MODES = ["idle", "listening", "thinking", "speaking"] as const;
const lab = (): { mode: OrbMode; size: number } | null => {
  const q = new URLSearchParams(location.search);
  if (!q.has("lab")) return null;
  return {
    mode: MODES.find((m) => m === q.get("mode")) ?? "idle",
    size: Number(q.get("size")) || 640,
  };
};

export function App() {
  const l = lab();
  if (l)
    return (
      <div className="lab">
        <Orb mode={l.mode} muted={false} size={l.size} />
      </div>
    );
  return (
    <div className="stage">
      <Phone forced={initialFrame()} />
    </div>
  );
}
