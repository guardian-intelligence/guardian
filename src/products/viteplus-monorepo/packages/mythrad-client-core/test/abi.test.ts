// The host binds two wasm modules by name, and TypeScript interfaces do
// not survive to runtime — so a call to an export the module lacks is
// not a type error, it is a "not a function" at the first frame, and an
// import the module expects but the host does not supply is an
// instantiation failure at boot. Neither announces itself until the code
// runs.
//
// These lists mirror the interfaces in src/abi.ts. Set equality against
// the committed modules turns ABI drift in either direction into a
// failing test at the moment it lands, in either repo.

import { describe, expect, it } from "vitest";
import { modules } from "./wasm.ts";

/** Exactly the members of `ClientExports`, minus `memory`. */
const CLIENT_EXPORTS = [
  "session_buf",
  "session_cap",
  "session_init",
  "session_reidentify",
  "session_connected",
  "session_disconnected",
  "session_on_stream",
  "session_on_datagram",
  "session_pump",
  "session_set_visible",
  "session_terrain_ready",
  "session_module_swapped",
  "session_tick",
  "session_seq",
  "session_stat",
  "session_phase_q16",
  "session_rtt_ms",
  "session_error_q16",
  "intent_join",
  "intent_check_in",
  "intent_move_to",
  "intent_boost",
  "frame_buf",
  "frame_cap",
  "smooth_frame",
];

/** Exactly the members of `HostImports`. */
const HOST_IMPORTS = [
  "park_apply",
  "park_step",
  "park_snapshot",
  "park_restore",
  "park_hash",
  "park_tick",
  "send_stream",
  "send_datagram",
  "inflate",
  "request",
  "emit",
];

/** Exactly the members of `ParkExports`, minus `memory`. */
const PARK_EXPORTS = [
  "io_buf",
  "io_cap",
  "terrain_buf",
  "terrain_cap",
  "sim_set_terrain",
  "sim_init",
  "sim_terrain_id",
  "sim_apply",
  "sim_step",
  "sim_snapshot",
  "sim_restore",
  "sim_hash",
  "sim_tick",
  "sim_view",
  "sim_hud",
];

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
