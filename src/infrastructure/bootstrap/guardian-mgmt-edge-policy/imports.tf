# One-time adoption of the cache-settings rulesets that predate this root's
# in-cluster R2 state: Cloudflare allows a single zone ruleset per phase, so
# the pre-existing rulesets (workstation applies whose state never migrated)
# hold the http_request_cache_settings slot in every zone and the first
# in-cluster apply failed on the phase cap. The config already describes
# them; post-import the plan reduces to in-place drift correction. IDs are
# Cloudflare identifiers, not secrets.
#
# Safe to delete this file once the root has converged Ready.

import {
  to = cloudflare_ruleset.cache_policy
  id = "zones/c952fb5989d232593ec9cca71030cb58/001a98d18c5c4065ac4cc5826dacb401"
}

import {
  to = cloudflare_ruleset.rumi_cache_policy
  id = "zones/034bf5d0a4ff33b0e9965f50be70d8d0/3b1790a6e63d464db456fdfc81ec451b"
}

import {
  to = cloudflare_ruleset.mythra_cache_policy
  id = "zones/4bfa5c0e3d0183dc0e30b9c4cbc17d47/66e7e8f396074fdb919abb700dcff372"
}
