// Boot: who is at the keyboard, what the park is serving, and the wiring
// that turns the two into a live session. The session core (docs/netcode.md)
// owns the protocol — ordering, rollback, resync, prediction, check
// cadence — so what is left here is identity, the platform errands the
// core asks for, and the controls.

import { Core, browserRandom32, type Ports, type RoleName } from "@guardian/mythrad-client-core";
import { OpenFeature } from "@openfeature/web-sdk";
import * as v from "valibot";
import {
  accessToken,
  beginSignIn,
  completeSignIn,
  deviceId,
  popupRelay,
  subjectOf,
  type SignInOutcome,
} from "./auth";
import { createDebugPanel, type DebugPanel } from "./debug";
import { createHud, type Hud } from "./hud";
import { createJank } from "./jank";
import { createRenderer } from "./renderer";
import {
  createTelemetry,
  jank as jankSpan,
  reportBootFailure,
  signInFailed,
  signedIn,
  tapSpans,
  unsupported,
} from "./telemetry";
import { createTransport, type Credentials } from "./transport";

const DEFAULT_PARK = "park-mythra";
const CHECK_SECONDS_FLAG = "netcode-check-seconds";
const DEFAULT_CHECK_SECONDS = 5;

const WtInfo = v.object({ parkWasm: v.string(), clientWasm: v.string() });

const FNV_OFFSET = 0xcbf29ce484222325n;
const FNV_PRIME = 0x100000001b3n;
const MASK64 = (1n << 64n) - 1n;

/** The dog a subject brings to the park: a stable 64-bit name for it. */
function fnv64(s: string): bigint {
  let h = FNV_OFFSET;
  for (const b of new TextEncoder().encode(s)) {
    h ^= BigInt(b);
    h = (h * FNV_PRIME) & MASK64;
  }
  return h;
}

export function startGame(): void {
  if (popupRelay()) return;
  void run(createHud());
}

async function run(hud: Hud): Promise<void> {
  let debug: DebugPanel | null = null;
  try {
    // Read after completeSignIn restores the pre-sign-in query, so ?park=
    // and ?spectate survive a redirect-flow round trip.
    const outcome = await completeSignIn();
    const query = new URLSearchParams(location.search);
    const spectate = query.has("spectate");
    const park = query.get("park") || DEFAULT_PARK;
    if (outcome.status === "error") {
      signInFailed(outcome.reason, "redirect");
      hud.log(`sign-in failed: ${outcome.reason}`);
    } else if (outcome.status === "signed-in") {
      signedIn("redirect");
    }

    // Who is at the keyboard: a WUM account (player) or the anonymous
    // device pseudonym (spectator). Both get deterministic feature flags
    // and trace identity keyed on the sub; the device id rides along after
    // sign-in so the acquisition-funnel join survives the upgrade.
    const identity = { anon: true, role: "spectator" as RoleName, myDog: 0n };
    const setIdentity = async (): Promise<void> => {
      const token = await accessToken();
      identity.anon = !token;
      const sub = token ? subjectOf(token) : `anon:${deviceId()}`;
      identity.role = identity.anon || spectate ? "spectator" : "player";
      identity.myDog = fnv64(sub);
      await OpenFeature.setContext({
        ...OpenFeature.getContext(),
        targetingKey: sub,
        device: deviceId(),
      });
      hud.setAnon(identity.anon);
    };
    await setIdentity();
    if (identity.anon) {
      hud.setWho("You're watching the park live — sign in to bring your dog.");
    }

    const telemetry = createTelemetry({ park, log: hud.log });
    const info = v.parse(WtInfo, await (await fetch("/wt-info", { cache: "no-store" })).json());

    if (typeof WebTransport === "undefined") {
      unsupported();
      hud.setStatus("unsupported");
      hud.setWho("This browser can't reach the dog park (no WebTransport).");
      return;
    }

    const credentials = async (): Promise<Credentials> => {
      if (!identity.anon) {
        const token = await accessToken();
        if (token) return { token, device: deviceId(), spectate };
        // The refresh token aged out mid-session: degrade to anonymous
        // spectating and surface the way back in. Adopting the anonymous
        // dog is a reidentify, which orphans this dial and starts a clean
        // one under the pseudonym.
        hud.log("session expired — spectating; sign in to rejoin with your dog");
        await setIdentity();
        core.reidentify(identity.myDog, identity.role);
      }
      return { token: null, device: deviceId(), spectate };
    };

    const ports: Ports = {
      transport: createTransport({ park, credentials, onDialed: telemetry.noteDial }),
      fetchBehavior: async (kind, ref) => {
        const r = await fetch(`/behavior/${kind}.wasm?v=${ref ?? Date.now()}`);
        if (!r.ok) throw new Error(`/behavior/${kind}.wasm ${r.status}`);
        return r.arrayBuffer();
      },
      fetchTerrain: async (idHex) => {
        const r = await fetch(`/terrain/${idHex}`);
        if (!r.ok) throw new Error(`/terrain/${idHex} ${r.status}`);
        return r.arrayBuffer();
      },
      now: () => Date.now(),
      telemetry: (code, a, b) => {
        telemetry.emit(code, a, b);
        debug?.onEmit(code, a, b);
      },
      random32: browserRandom32,
      log: hud.log,
    };

    const core = new Core(ports, {
      myDog: identity.myDog,
      role: identity.role,
      park,
      // The cadence is read once: it paces the session core, which takes
      // it at init. A flag flip reaches open pages on their next load.
      checkMs: Math.round(
        OpenFeature.getClient().getNumberValue(CHECK_SECONDS_FLAG, DEFAULT_CHECK_SECONDS) * 1000,
      ),
      moduleRefs: { park: info.parkWasm, client: info.clientWasm },
    });
    hud.bind(core);
    // The core is idle until it dials, but the page has been promising a
    // connection since it rendered, and the modules load in between.
    hud.setStatus("connecting");
    telemetry.bind(core);
    debug = import.meta.env.DEV ? createDebugPanel(core) : null;
    // The raw per-frame ring is opt-in because only a harness reads it; the
    // counters behind the once-a-minute span always run.
    const probe = query.has("probe");
    const jank = createJank({ probe, emit: (counters) => jankSpan(park, counters) });
    // Under the probe the harness reads the spans alongside the frames:
    // the frames show a rewind, the spans say which repair asked for it.
    if (probe) tapSpans(jank.recordSpan);
    const renderer = createRenderer({ core, hud, jank, myDog: () => identity.myDog });

    const onSignIn = async (o: SignInOutcome): Promise<void> => {
      if (o.status === "error") {
        signInFailed(o.reason, "popup");
        hud.log(`sign-in failed: ${o.reason}`);
        return;
      }
      if (o.status !== "signed-in") return;
      await setIdentity();
      signedIn("popup");
      hud.log("signed in — bringing your dog to the park");
      // The replica state carries over, so the upgrade is a reconnect
      // under the new identity rather than a reload.
      core.reidentify(identity.myDog, identity.role);
    };

    hud.controls.signin.onclick = () =>
      beginSignIn("google", (o) => {
        void onSignIn(o);
      });
    hud.controls.checkin.onclick = () => {
      core.checkIn();
    };
    hud.controls.recenter.onclick = () => renderer.followCamera();
    // Boost is a held button. Release insurance rides every channel a
    // browser can lose a pointerup on; a disconnect needs none — the
    // departure staged on disconnect removes the dog, boost bit and all.
    const boost = hud.controls.boost;
    boost.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      core.setBoost(true);
    });
    for (const ev of ["pointerup", "pointercancel", "pointerleave"]) {
      boost.addEventListener(ev, () => {
        core.setBoost(false);
      });
    }
    window.addEventListener("blur", () => {
      core.setBoost(false);
    });
    document.addEventListener("visibilitychange", () => {
      // Hidden pages go quiet: the core stops sending check datagrams.
      core.setVisible(!document.hidden);
      if (document.hidden) core.setBoost(false);
    });

    const frame = (now: number): void => {
      requestAnimationFrame(frame);
      // A frozen pump is a zero step budget, not a skipped call: the clock
      // keeps observing and events keep applying, the replica just stops
      // advancing. Skipping would stop the session dead.
      if (debug?.frozen()) core.pump(0);
      else core.pump();
      renderer.frame(now);
      debug?.update();
    };
    requestAnimationFrame(frame);

    hud.log(identity.role === "player" ? "joining with your dog" : "spectating — sign in to join");
    await core.boot();
    // A page that loaded in a background tab has never had a
    // visibilitychange to tell the core it is not being watched.
    core.setVisible(!document.hidden);
  } catch (e) {
    const id = reportBootFailure(e);
    const detail = e instanceof Error ? e.message : String(e);
    hud.setStatus("error");
    hud.setWho(`The park didn't load (${detail}) — refresh to retry.`);
    hud.log(`boot failed: ${detail} [err ${id}]`);
  }
}
