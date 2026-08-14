export * from "./wire.ts";
export * from "./fakes.ts";
export * from "./wasm.ts";
// `Reject` is both wire.ts's frame payload type and wasm.ts's reject-code
// table; the rig's table is the one the suites name.
export { Reject } from "./wasm.ts";
