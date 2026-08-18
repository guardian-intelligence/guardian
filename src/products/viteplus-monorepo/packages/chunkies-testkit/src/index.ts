export * from "./wire.ts";
// v5 lives behind a namespace: its message names intentionally shadow the
// v4 exports above, and the two protocols must never be mixed in one
// harness by accident. v4 dies with the flag day; this namespace flattens
// then.
export * as wire5 from "./wire5.ts";
export * from "./fakes.ts";
export * from "./wasm.ts";
// `Reject` is both wire.ts's frame payload type and wasm.ts's reject-code
// table; the rig's table is the one the suites name.
export { Reject } from "./wasm.ts";
