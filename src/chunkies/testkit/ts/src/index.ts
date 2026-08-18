export * from "./wire5.ts";
export * from "./fakes.ts";
export * from "./wasm.ts";
// `Reject` is both a frame payload type and wasm.ts's reject-code table;
// the rig's table is the one the suites name. The conformance suite names
// the codec as a namespace; everything else gets the flat exports.
export { Reject } from "./wasm.ts";
export * as wire5 from "./wire5.ts";
