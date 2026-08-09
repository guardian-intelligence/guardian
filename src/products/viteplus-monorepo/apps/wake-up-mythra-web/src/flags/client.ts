// Client feature flags: the OpenFeature web SDK evaluating over OFREP (the
// vendor-neutral OpenFeature HTTP protocol) against the guardian flag
// control plane, mounted same-origin at /features by the apex Ingress.
// Values are evaluated server-side per context, so targeting rules never
// ship to the browser. Change notification is a read-only SSE subscription
// to /features/events carrying only the flag-set epoch; on a change the
// evaluation context is bumped, the provider re-evaluates, and subscribed
// components re-render — no reload, same session, every open client within
// about a second of the fan-out. Flags are presentation-only by house
// rule: they never reach the sim, whose behavior changes ship as wasm
// modules through the shadow-launch lane.
import { OFREPWebProvider } from "@openfeature/ofrep-web-provider";
import { OpenFeature, type Provider } from "@openfeature/web-sdk";

let started = false;

export function startFlags(): void {
  if (started) return;
  started = true;
  // Polling stays at the provider default (disabled): the SSE stream plus
  // the provider's built-in refresh-on-tab-visible cover everything a poll
  // would. Dev talks straight to a local flagd's OFREP port — run it with
  // --cors-allowed-origins='*' — and skips the stream (no notify service).
  const dev = import.meta.env.DEV;
  // The cast bridges exactOptionalPropertyTypes: the provider declares
  // `hooks?: Hook[] | undefined` where the SDK's Provider forbids an
  // explicit undefined; the runtime shape is identical.
  OpenFeature.setProvider(
    new OFREPWebProvider({
      baseUrl: dev ? "http://127.0.0.1:8016" : "/features",
    }) as unknown as Provider,
  );
  if (dev) return;

  const events = new EventSource("/features/events");
  let epoch: string | null = null;
  events.addEventListener("epoch", (e) => {
    const next = (e as MessageEvent<string>).data;
    if (epoch === next) return;
    epoch = next;
    // Bumping the context makes the provider re-evaluate every flag. The
    // first event also lands here on purpose: it closes the race where the
    // flag set changed between the provider's initial fetch and the stream
    // opening, at the cost of one extra evaluation per page load.
    void OpenFeature.setContext({ ...OpenFeature.getContext(), flagSetEpoch: next });
  });
}
