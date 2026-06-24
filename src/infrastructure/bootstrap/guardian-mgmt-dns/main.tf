locals {
  cloudflare_zone_name  = "guardianintelligence.org"
  external_dns_owner_id = "guardian-mgmt-ash"
  public_ingress_origin_names = [
    "ash-earth",
    "ash-wind",
    "ash-water",
  ]
  public_edge_hostnames = [
    "*.guardianintelligence.org",
    "guardianintelligence.org",
    "api.guardianintelligence.org",
    "alerta.guardianintelligence.org",
    "dashboard.guardianintelligence.org",
    "grafana.guardianintelligence.org",
    "harbor.guardianintelligence.org",
    "keycloak.guardianintelligence.org",
    "s3.guardianintelligence.org",
  ]

  public_ingress_ipv4s = [
    for name in local.public_ingress_origin_names :
    data.terraform_remote_state.guardian_mgmt.outputs.control_plane_nodes[name].public_ipv4
  ]
}

data "terraform_remote_state" "guardian_mgmt" {
  backend = "s3"
  config = {
    bucket = "guardian-vault"
    key    = "opentofu/guardian-mgmt.tfstate"
    region = "auto"

    endpoint                    = "https://${var.cloudflare_account_id}.r2.cloudflarestorage.com"
    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_path_style              = true
  }
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
      address = data.terraform_remote_state.guardian_mgmt.outputs.control_plane_nodes[origins.value].public_ipv4
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
  proxied              = true
  steering_policy      = "off"
  session_affinity     = "cookie"
  session_affinity_ttl = 1800

  session_affinity_attributes {
    secure                 = "Always"
    samesite               = "Auto"
    zero_downtime_failover = "temporary"
  }

  adaptive_routing {
    failover_across_pools = false
  }
}

check "cloudflare_load_balancer_hostnames" {
  assert {
    condition     = length(local.public_edge_hostnames) == 9
    error_message = "Root public edge hostnames belong to Cloudflare Load Balancing."
  }
}

check "cloudflare_load_balancer_origins" {
  assert {
    condition     = length(local.public_ingress_ipv4s) == 3
    error_message = "Cloudflare Load Balancing must publish all three guardian-mgmt ASH control-plane origins."
  }
}
