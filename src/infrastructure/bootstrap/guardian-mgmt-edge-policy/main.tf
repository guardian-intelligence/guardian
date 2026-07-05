locals {
  cloudflare_zone_name = "guardianintelligence.org"
}

data "cloudflare_zone" "guardianintelligence_org" {
  name       = local.cloudflare_zone_name
  account_id = var.cloudflare_account_id
}

# Zone-level Authenticated Origin Pulls: the edge presents Cloudflare's
# managed client certificate on every origin fetch. Enabling this is
# non-breaking on its own; enforcement lives at the origin, where
# ingress-nginx verifies the certificate and refuses direct-to-origin
# traffic. Only after that verification is CF-Connecting-IP trustworthy
# enough to map into x-guardian-client-ip.
resource "cloudflare_authenticated_origin_pulls" "guardianintelligence_org" {
  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  enabled = true
}

# Bot Fight Mode is unscopeable (no path/hostname exemptions) and challenges
# first-party POST beacons, which silently drops analytics and fraud events.
# Abuse filtering belongs to the provenance/trust-tier pipeline where every
# decision is recorded and inspectable, not to an opaque edge toggle.
resource "cloudflare_bot_management" "guardianintelligence_org" {
  zone_id    = data.cloudflare_zone.guardianintelligence_org.id
  fight_mode = false
}

# The edge must never cache API responses: event beacons, RPCs, and anything
# else under /api/ are per-visitor and often authenticated. Cloudflare does
# not cache HTML by default, so /api/ is the one surface that needs an
# explicit rule today; add rules here rather than dashboard cache settings.
resource "cloudflare_ruleset" "cache_policy" {
  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  name    = "guardian edge cache policy"
  kind    = "zone"
  phase   = "http_request_cache_settings"

  rules {
    ref         = "bypass_api_cache"
    description = "Never edge-cache /api/ (event beacons, RPCs)"
    expression  = "(starts_with(http.request.uri.path, \"/api/\"))"
    action      = "set_cache_settings"
    enabled     = true

    action_parameters {
      cache = false
    }
  }
}
