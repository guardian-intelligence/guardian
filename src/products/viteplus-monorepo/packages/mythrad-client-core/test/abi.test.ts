// The host binds two wasm modules by name, and TypeScript interfaces do
// not survive to runtime — so a call to an export the module lacks is
// not a type error, it is a "not a function" at the first frame, and an
// import the module expects but the host does not supply is an
// instantiation failure at boot. Neither announces itself until the code
// runs.
//
// The name tables live in src/abi.ts, where `Core.boot` verifies them
// against the fetched modules before any cast. Set equality against the
// committed modules turns ABI drift in either direction into a failing
// test at the moment it lands, in either repo.

import { describe, expect, it } from "vitest";
import { CLIENT_EXPORTS, HOST_IMPORTS, PARK_EXPORTS } from "../src/abi.ts";
import { ActionKind, Emit, HostEmit } from "../src/ports.ts";
import { bringTheDogIn, dogPayload, Ev, modules, rig } from "@guardian/chunkies-testkit";

/** Park exports the host has no use for, but the module legitimately has. */
const PARK_UNUSED = ["sim_epoch", "sim_rate", "sim_anchor_tick", "sim_anchor_ns"];

function names(list: { name: string }[]): Set<string> {
  return new Set(list.map((e) => e.name));
}

describe("client.wasm", () => {
  const mod = new WebAssembly.Module(modules().client.slice().buffer);
  const exports = names(WebAssembly.Module.exports(mod));
  const imports = WebAssembly.Module.imports(mod);

  it("exports a linear memory for the host to stage bytes in", () => {
    expect(WebAssembly.Module.exports(mod).find((e) => e.name === "memory")?.kind).toBe("memory");
  });

  it("exports exactly what ClientExports declares", () => {
    expect([...exports].sort()).toEqual([...CLIENT_EXPORTS, "memory"].sort());
  });

  it("imports exactly the eleven host functions, all from module `host`", () => {
    expect(imports).toHaveLength(HOST_IMPORTS.length);
    expect([...new Set(imports.map((i) => i.module))]).toEqual(["host"]);
    expect(imports.map((i) => i.name).sort()).toEqual([...HOST_IMPORTS].sort());
    expect(imports.every((i) => i.kind === "function")).toBe(true);
  });
});

describe("park.wasm", () => {
  const mod = new WebAssembly.Module(modules().park.slice().buffer);
  const exports = names(WebAssembly.Module.exports(mod));

  it("imports nothing: the sim is a pure function of its own memory", () => {
    expect(WebAssembly.Module.imports(mod)).toHaveLength(0);
  });

  it("exports everything ParkExports declares", () => {
    for (const name of PARK_EXPORTS) expect(exports.has(name), name).toBe(true);
  });

  it("declares no park export the module does not have", () => {
    expect([...exports].sort()).toEqual([...PARK_EXPORTS, ...PARK_UNUSED, "memory"].sort());
  });
});

describe("the telemetry vocabulary", () => {
  it("names every code the core emits", async () => {
    // The emit codes are a contract the Rust crate owns and this file
    // mirrors. Nothing in the module's import or export tables carries
    // them, so a code added on the other side arrives here as a bare
    // integer: no name in a dashboard, no case in a switch, and no
    // failure anywhere. This is the only thing that notices.
    const named = new Set<number>([...Object.values(Emit), ...Object.values(HostEmit)]);
    const r = await rig({ role: "player", myDog: 0x9801n });
    await r.establish();
    await bringTheDogIn(r, 0x9801n);

    // Exercise enough paths that the common vocabulary shows up: an
    // intent, its answer, a repair, a check and its verdict.
    r.core.checkIn();
    r.core.setBoost(true);
    await r.run(300);
    r.answerChecks();
    const arrival = r.authority.apply(Ev.join, dogPayload(0x9802n));
    await r.run(100);
    r.deliver([arrival]);
    await r.run(600);
    r.answerChecks();
    await r.run(600);

    const seen = new Set(r.harness.emitted.map((e) => e.code));
    const unnamed = [...seen].filter((code) => !named.has(code)).sort((a, b) => a - b);
    expect(seen.size, "codes observed").toBeGreaterThan(5);
    expect(unnamed, "telemetry codes this host cannot name").toEqual([]);
  });

  it("names every action kind a host can send", async () => {
    // The action verbs are the host's whole write surface, and the kind
    // rides every action span as a bare number. A verb whose kind has no
    // row in ActionKind reaches dashboards as "kind N" — this is the only
    // thing that notices the table going stale.
    const r = await rig({ role: "player", myDog: 0x9803n });
    await r.establish();
    await bringTheDogIn(r, 0x9803n);
    r.core.checkIn();
    r.core.moveTo(1);
    r.core.setBoost(true);
    await r.run(300);

    const sent = r.harness.emitted
      .filter((e) => e.code === Emit.intentSent)
      .map((e) => Number(e.a));
    expect(new Set(sent).size, "distinct kinds exercised").toBeGreaterThanOrEqual(4);
    const unnamed = [...new Set(sent)].filter((kind) => !(kind in ActionKind));
    expect(unnamed, "action kinds without a name").toEqual([]);
  });
});
