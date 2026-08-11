import { OpenFeatureProvider } from "@openfeature/react-sdk";
import { createFileRoute } from "@tanstack/react-router";
import { useEffect } from "react";
import { startFlags } from "~/flags/client";
import { FlagsCanary } from "~/flags/canary";
import { startGame } from "~/game/client";

export const Route = createFileRoute("/")({
  component: Page,
});

// The game client owns the DOM below imperatively (canvas, pills, log)
// and survives route-component re-renders; start it exactly once per page.
let started = false;

function Page() {
  useEffect(() => {
    if (started) return;
    started = true;
    startFlags();
    startGame();
  }, []);
  return (
    <OpenFeatureProvider>
      <FlagsCanary />
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
          world <b id="world">–</b>
        </span>
        <span className="stat">
          downlink <b id="bytes">–</b>
        </span>
        <span className="stat">
          park energy <b id="energy">0</b>
        </span>
        <span className="stat dim" id="role"></span>
        <button id="signin" style={{ display: "none" }}>
          Continue with Google
        </button>
        <button id="checkin" style={{ display: "none" }}>
          Check in
        </button>
      </div>
      <canvas id="grid" width={100} height={100}></canvas>
      <div className="who" id="who"></div>
      <div id="log"></div>
    </OpenFeatureProvider>
  );
}
