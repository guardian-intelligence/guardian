# chunkies deploy components

Framework-owned kustomize shapes for the workloads every chunkies game runs.
A game never edits these; its own deploy tree composes them and patches in
everything instance-shaped. The topology hierarchy these implement is
documented in docs/netcode.md ("Topology").

## The shapes

- `components/gateway` — the per-node public gateway: WebTransport
  termination, ticketed admission, (game, chunk) routing via the hot-reloaded
  chunk directory. Framework-owned: image pin, ports, probes, security
  posture, rollout strategy.
- `components/chunkie` — one pod owning one or more chunks of a game's
  world (WUM's chunks are parks). Framework-owned: image pin, probes,
  security posture, and the single-writer rollout strategy (maxSurge 0 —
  one journal writer per chunk, always). Chunkies never canary; they roll by
  checkpoint/restore.

## How a game consumes them

See src/games/wake-up-mythra/deploy/prod for the live example:

- `topology/directory.conf` — the game's chunk directory lines
  (`<game> <chunk> <authority addr>`), merged into the `chunkies-directory`
  ConfigMap by the overlay's configMapGenerator. The gateway hot-reloads it:
  topology changes must never restart the process holding live sessions.
- `topology/chunkies/<name>/` — one dir per chunkie instance: the
  component as its resource plus one patch carrying placement
  (nodeSelector/tolerations), resources, game labels, env, and volumes.
  A second instance is a sibling dir with a nameSuffix.
- The overlay's own kustomization composes the gateway component the same
  way with a gateway patch.

Env and volumes are patched wholesale for now, a deliberate interim: the
gateway is single-game, so the whole env is instance config. When
multi-game admission lands, the framework env moves into the components and
game patches shrink to their own vocabulary.

## The acceptance test

Launching a new game touches `src/games/<name>/` (sim, client, content,
deploy tree), one Flux Kustomization entry, a DNS record, and a realm
client — and nothing under this directory. If adding a game requires
editing a component, the seam is in the wrong place.
