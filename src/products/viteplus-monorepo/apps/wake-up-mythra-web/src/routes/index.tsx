import { OpenFeatureProvider } from "@openfeature/react-sdk";
import { createFileRoute } from "@tanstack/react-router";
import { useEffect } from "react";
import { startFlags } from "~/flags/client";
import { FlagsCanary } from "~/flags/canary";

export const Route = createFileRoute("/")({
  component: Page,
});

let started = false;

function Page() {
  useEffect(() => {
    if (started) return;
    started = true;
    startFlags();
  }, []);
  return (
    <OpenFeatureProvider>
      <FlagsCanary />
      <div className="bar">
        <span id="status" className="pill">
          CLOSED FOR CONSTRUCTION
        </span>
      </div>
      <div className="who">
        The dog park is closed while we rebuild it. The dogs are fine — they are at home, napping.
        Back soon.
      </div>
    </OpenFeatureProvider>
  );
}
