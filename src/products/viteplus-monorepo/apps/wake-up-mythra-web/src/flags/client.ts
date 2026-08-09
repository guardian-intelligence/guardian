// Client feature flags: the OpenFeature web SDK subscribed to the guardian
// flag control plane (flagd), mounted same-origin at /flags by the apex
// Ingress. The provider holds a Connect server-stream; a flag flip in git
// converges to the ConfigMap, flagd hot-reloads it, and every connected
// page re-renders — no reload, same session. Flags are presentation-only
// by house rule: they never reach the sim, whose behavior changes ship as
// wasm modules through the shadow-launch lane.
import { FlagdWebProvider } from "@openfeature/flagd-web-provider";
import { OpenFeature } from "@openfeature/web-sdk";

let started = false;

export function startFlags(): void {
  if (started) return;
  started = true;
  // Dev talks straight to a locally running flagd (CORS-open); prod rides
  // the same-origin /flags mount. pathPrefix has no leading slash — the
  // provider inserts the separator itself.
  const options = import.meta.env.DEV
    ? { host: "127.0.0.1", port: 8013, tls: false, maxRetries: 0, maxDelay: 30000 }
    : {
        host: location.hostname,
        port: 443,
        tls: true,
        pathPrefix: "flags",
        maxRetries: 0,
        maxDelay: 30000,
      };
  OpenFeature.setProvider(new FlagdWebProvider(options));
}
