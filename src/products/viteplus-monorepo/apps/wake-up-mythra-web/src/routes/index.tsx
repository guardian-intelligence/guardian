import { createFileRoute } from "@tanstack/react-router";
import { useEffect } from "react";
import { startPresence } from "~/game/presence";

export const Route = createFileRoute("/")({
  component: Page,
});

// The presence client owns the DOM below imperatively (canvas, pills, log)
// and survives route-component re-renders; start it exactly once per page.
let started = false;

function Page() {
  useEffect(() => {
    if (started) return;
    started = true;
    startPresence();
  }, []);
  return (
    <>
      <div className="bar">
        <span id="status" className="pill">
          CONNECTING
        </span>
        <span className="stat">
          tick <b id="tick">–</b>
        </span>
        <span className="stat">
          rtt <b id="rtt">–</b>
        </span>
        <span className="stat">
          sim behavior <b id="behavior">–</b>
        </span>
        <span className="stat">
          client sim <b id="client">–</b>
        </span>
        <span className="stat">
          world <b id="world">–</b>
        </span>
        <span className="stat">
          skin <b id="skin">cell</b>
        </span>
        <span className="stat dim" id="role"></span>
      </div>
      <canvas id="grid" width={100} height={100}></canvas>
      <div className="who" id="who"></div>
      <div id="log"></div>
    </>
  );
}
