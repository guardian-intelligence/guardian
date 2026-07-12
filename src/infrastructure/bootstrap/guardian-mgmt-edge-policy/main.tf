locals {
  cloudflare_zone_name = "guardianintelligence.org"
}

data "cloudflare_zone" "guardianintelligence_org" {
  filter = {
    name = local.cloudflare_zone_name
    account = {
      id = var.cloudflare_account_id
    }
  }
}

# Zone-level Authenticated Origin Pulls: the edge presents Cloudflare's
# managed client certificate on every origin fetch. Enabling this is
# non-breaking on its own; enforcement lives at the origin, where
# ingress-nginx verifies the certificate and refuses direct-to-origin
# traffic. Only after that verification is CF-Connecting-IP trustworthy
# enough to map into x-guardian-client-ip.
resource "cloudflare_authenticated_origin_pulls_settings" "guardianintelligence_org" {
  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  enabled = true
}

resource "cloudflare_zone_setting" "origin_ssl" {
  zone_id    = data.cloudflare_zone.guardianintelligence_org.id
  setting_id = "ssl"
  value      = "strict"
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
# not cache JSON API paths by default, so cacheable API surfaces (Electric
# shapes) need an explicit eligibility rule and per-visitor surfaces need an
# explicit bypass; add rules here rather than dashboard cache settings.
resource "cloudflare_ruleset" "cache_policy" {
  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  name    = "guardian edge cache policy"
  kind    = "zone"
  phase   = "http_request_cache_settings"

  rules = [
    {
      ref         = "bypass_api_cache"
      description = "Never edge-cache /api/ (event beacons, RPCs)"
      expression  = "(starts_with(http.request.uri.path, \"/api/\"))"
      action      = "set_cache_settings"
      enabled     = true

      action_parameters = {
        cache = false
      }
    },
    {
      ref         = "electric_shape_cache"
      description = "Electric shape API: edge-cache per origin Cache-Control (request collapsing for cockpit reads)"
      expression  = "(http.host in {\"guardianintelligence.org\" \"beta.guardianintelligence.org\" \"gamma.guardianintelligence.org\"}) and starts_with(http.request.uri.path, \"/electric/v1/shape\")"
      action      = "set_cache_settings"
      enabled     = true

      action_parameters = {
        cache = true

        edge_ttl = {
          mode = "respect_origin"
        }

        browser_ttl = {
          mode = "respect_origin"
        }
      }
    },
  ]
}
