locals {
  cloudflare_zone_name  = "guardianintelligence.org"
  external_dns_owner_id = "guardian-mgmt-ash"
  public_ingress_origin_names = [
    "ash-earth",
    "ash-wind",
    "ash-water",
  ]
  public_ingress_origins = {
    ash-earth = {
      public_ipv4 = "206.223.228.101"
    }
    ash-wind = {
      public_ipv4 = "45.250.254.119"
    }
    ash-water = {
      public_ipv4 = "206.223.228.87"
    }
  }
  public_edge_hostnames = [
    "*.guardianintelligence.org",
    "guardianintelligence.org",
    "api.guardianintelligence.org",
    "alerta.guardianintelligence.org",
    "dashboard.guardianintelligence.org",
    "grafana.guardianintelligence.org",
    "keycloak.guardianintelligence.org",
  ]

  public_ingress_ipv4s = [
    for name in local.public_ingress_origin_names :
    local.public_ingress_origins[name].public_ipv4
  ]
}

data "cloudflare_zone" "guardianintelligence_org" {
  name       = local.cloudflare_zone_name
  account_id = var.cloudflare_account_id
}

resource "cloudflare_load_balancer_monitor" "guardian_mgmt_ingress" {
  account_id       = var.cloudflare_account_id
  description      = "guardian-mgmt ASH root ingress HTTPS health"
  type             = "https"
  method           = "GET"
  path             = "/"
  expected_codes   = "200,302"
  follow_redirects = false
  interval         = var.cloudflare_lb_monitor_interval_seconds
  retries          = 1
  timeout          = 5
  consecutive_down = 1
  consecutive_up   = 1
  probe_zone       = local.cloudflare_zone_name

  header {
    header = "Host"
    values = [
      "dashboard.guardianintelligence.org",
    ]
  }
}

resource "cloudflare_load_balancer_pool" "guardian_mgmt_ash" {
  account_id      = var.cloudflare_account_id
  name            = "guardian-mgmt-ash"
  description     = "Guardian management cluster ASH public ingress origins"
  enabled         = true
  minimum_origins = 1
  monitor         = cloudflare_load_balancer_monitor.guardian_mgmt_ingress.id
  check_regions   = var.cloudflare_lb_check_regions

  dynamic "origins" {
    for_each = toset(local.public_ingress_origin_names)

    content {
      name    = origins.value
      address = local.public_ingress_origins[origins.value].public_ipv4
      enabled = true
      weight  = 1
    }
  }

  origin_steering {
    policy = "random"
  }
}

resource "cloudflare_load_balancer" "guardian_mgmt_public" {
  for_each = toset(local.public_edge_hostnames)

  zone_id          = data.cloudflare_zone.guardianintelligence_org.id
  name             = each.value
  description      = "guardian-mgmt ASH public edge"
  enabled          = true
  fallback_pool_id = cloudflare_load_balancer_pool.guardian_mgmt_ash.id
  default_pool_ids = [
    cloudflare_load_balancer_pool.guardian_mgmt_ash.id,
  ]
  proxied         = true
  steering_policy = "off"

  adaptive_routing {
    failover_across_pools = false
  }
}

resource "cloudflare_ruleset" "guardian_edge_cache_policy" {
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

  rules {
    ref         = "electric_shape_cache"
    description = "Electric shape API: edge-cache per origin Cache-Control (request collapsing for cockpit reads)"
    expression  = "(http.host in {\"guardianintelligence.org\" \"beta.guardianintelligence.org\" \"gamma.guardianintelligence.org\"}) and starts_with(http.request.uri.path, \"/electric/v1/shape\")"
    action      = "set_cache_settings"
    enabled     = true

    action_parameters {
      cache = true

      edge_ttl {
        mode = "respect_origin"
      }

      browser_ttl {
        mode = "respect_origin"
      }
    }
  }
}

check "cloudflare_load_balancer_hostnames" {
  assert {
    condition     = length(local.public_edge_hostnames) == 7
    error_message = "Root public edge hostnames belong to Cloudflare Load Balancing."
  }
}

check "cloudflare_load_balancer_origins" {
  assert {
    condition     = length(local.public_ingress_ipv4s) == 3
    error_message = "Cloudflare Load Balancing must publish all three guardian-mgmt ASH control-plane origins."
  }
}
