# Canary share links

Real share links used by the release gate (docs/runbooks/aisucks-release.md)
and the live parser test (`CANARY_URL=… go test -run TestCanary`). They must
stay live: do not delete the underlying conversations.

| provider | link | note |
|---|---|---|
| chatgpt | https://chatgpt.com/share/6a2a4ce7-8e14-83e8-acef-fd7cf9a144fe | provided 2026-06-11; flight-format payload |
| claude | — | not needed until Claude support ships (post-launch) |

A canary submission during the gate lands in that environment's database —
expected; the duplicate page on re-submission is part of the check.
