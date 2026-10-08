import { STAGES, type Stage } from "./content.ts";
import { Phone } from "./ui/Phone.tsx";

// ?frame=booked opens frozen on a frame (handy for screenshots and sharing a state).
const initialFrame = (): Stage | null => {
  const f = new URLSearchParams(location.search).get("frame");
  return STAGES.find((s) => s === f) ?? null;
};

export function App() {
  return (
    <div className="stage">
      <Phone forced={initialFrame()} />
    </div>
  );
}
