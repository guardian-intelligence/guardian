// The host binds two wasm modules by name, and TypeScript interfaces do
// not survive to runtime — so a call to an export the module lacks is
// not a type error, it is a "not a function" at the first frame, and an
// import the module expects but the host does not supply is an
// instantiation failure at boot. Neither announces itself until the code
// runs.
//
// The framework name tables live in src/abi.ts, where `ReplicaHost.boot`
// verifies them against the fetched modules before any cast; the game's
// extension and projection exports go through the guarded doors instead
// and are named here only so set equality against the committed modules
// still turns ABI drift in either direction into a failing test at the
// moment it lands, in either repo.

import { describe, expect, it } from "vitest";
import { HOST_IMPORTS, REPLICA_EXPORTS, SESSION_EXPORTS } from "../src/abi.ts";
import { HostEmit } from "../src/ports.ts";
import { Emit } from "../src/telemetry.gen.ts";
import { bringTheDogIn, dogPayload, Ev, modules, rig } from "@guardian/chunkies-testkit";

/** The committed session module's extension exports: WUM's intent verbs. */
const SESSION_EXTENSIONS = ["intent_join", "intent_check_in", "intent_move_to", "intent_boost"];

/** The committed replica module's projection exports: WUM's read surfaces. */
const REPLICA_PROJECTIONS = ["sim_view", "sim_hud"];

/** Replica exports the host has no use for, but the module legitimately has. */
const REPLICA_UNUSED = ["sim_epoch", "sim_rate", "sim_anchor_tick", "sim_anchor_ns"];

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

  it("exports exactly the framework surface plus the named extensions", () => {
    expect([...exports].sort()).toEqual(
      [...SESSION_EXPORTS, ...SESSION_EXTENSIONS, "memory"].sort(),
    );
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

  it("exports everything ReplicaExports declares", () => {
    for (const name of REPLICA_EXPORTS) expect(exports.has(name), name).toBe(true);
  });

  it("declares no replica export the module does not have", () => {
    expect([...exports].sort()).toEqual(
      [...REPLICA_EXPORTS, ...REPLICA_PROJECTIONS, ...REPLICA_UNUSED, "memory"].sort(),
    );
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
    r.checkIn();
    r.setBoost(true);
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
});
