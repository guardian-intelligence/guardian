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

  k8s_api_hostname = "k8s.${local.cloudflare_zone_name}"
}

data "cloudflare_zone" "guardianintelligence_org" {
  filter = {
    name = local.cloudflare_zone_name
    account = {
      id = var.cloudflare_account_id
    }
  }
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

  header = {
    Host = [
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

  # The API returns pool origins sorted by name and origins is an ordered
  # list, so the config must emit the same order or every plan is a reorder.
  origins = [
    for name in sort(local.public_ingress_origin_names) : {
      name    = name
      address = local.public_ingress_origins[name].public_ipv4
      enabled = true
      weight  = 1
    }
  ]

  origin_steering = {
    policy = "random"
  }
}

resource "cloudflare_load_balancer" "guardian_mgmt_public" {
  for_each = toset(local.public_edge_hostnames)

  zone_id       = data.cloudflare_zone.guardianintelligence_org.id
  name          = each.value
  description   = "guardian-mgmt ASH public edge"
  enabled       = true
  fallback_pool = cloudflare_load_balancer_pool.guardian_mgmt_ash.id
  default_pools = [
    cloudflare_load_balancer_pool.guardian_mgmt_ash.id,
  ]
  proxied         = true
  steering_policy = "off"

  adaptive_routing = {
    failover_across_pools = false
  }
}

# Must resolve while the cluster is down, so not in-cluster ExternalDNS;
# Cloudflare cannot proxy the Kubernetes/Talos API ports, so proxied=false.
resource "cloudflare_dns_record" "guardian_mgmt_k8s_api" {
  for_each = local.public_ingress_origins

  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  name    = local.k8s_api_hostname
  type    = "A"
  content = each.value.public_ipv4
  ttl     = 300
  proxied = false
  comment = "guardian-mgmt ${each.key} control-plane API"
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
