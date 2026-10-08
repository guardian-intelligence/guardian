import { useCallback, useState } from "react";

import { STAGES, type Stage } from "./content.ts";
import { Studio } from "./studio/Studio.tsx";
import { Phone } from "./ui/Phone.tsx";

// ?frame=booked opens frozen on a frame (handy for screenshots and sharing a state).
const initialFrame = (): Stage | null => {
  const f = new URLSearchParams(location.search).get("frame");
  return STAGES.find((s) => s === f) ?? null;
};
// The studio sits beside the phone in dev; elsewhere add ?studio to see it.
const studioWanted = () =>
  import.meta.env.DEV || new URLSearchParams(location.search).has("studio");

export function App() {
  const [forced, setForced] = useState<Stage | null>(initialFrame);
  const [restartKey, setRestartKey] = useState(0);
  const [gpuError, setGpuError] = useState<string | null>(null);
  const onGpuError = useCallback((msg: string | null) => setGpuError(msg), []);

  return (
    <div className="stage">
      <Phone forced={forced} restartKey={restartKey} onGpuError={onGpuError} />
      {studioWanted() && (
        <Studio
          forced={forced}
          onForce={setForced}
          onRestart={() => setRestartKey((k) => k + 1)}
          gpuError={gpuError}
        />
      )}
    </div>
  );
}
