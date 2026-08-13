# Mythra sim workspace

Crates are grouped by which side of the WebTransport boundary *executes*
them. Distribution doesn't matter — mythrad serves `client.wasm` bytes it
never runs.

- `shared/` — runs byte-identically on the server (wazero) and every
  client: fixed-point kernel, terrain, nav, and the park game state
  machine. Anything that can influence `sim_hash` must live here.
- `client/` — executed only by clients: presentation timing and netcode
  policy (clock discipline, frame smoothing). Reads sim state, never
  defines it.
- `server/` — executed only by mythrad's live/shadow behavior slot.

Placing a new crate: if it can influence `sim_hash`, it goes in `shared/`;
if it only reads sim state or sets client-side policy, `client/`;
authority-side behaviors, `server/`. Bazel visibility enforces the
boundary — a cross-tier dependency is a build error, not a review comment.
