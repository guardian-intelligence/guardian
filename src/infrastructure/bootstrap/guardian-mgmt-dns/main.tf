locals {
  route53_zone_name    = "gi.org"
  cloudflare_zone_name = "guardianintelligence.org"
  public_ingress_node  = "ash-earth"

  public_ingress_ipv4s = [
    data.terraform_remote_state.guardian_mgmt.outputs.control_plane_nodes[local.public_ingress_node].public_ipv4
  ]

  route53_record_sets = {}

  cloudflare_record_sets = {
    "guardianintelligence.org"           = local.public_ingress_ipv4s
    "api.guardianintelligence.org"       = local.public_ingress_ipv4s
    "dashboard.guardianintelligence.org" = local.public_ingress_ipv4s
    "grafana.guardianintelligence.org"   = local.public_ingress_ipv4s
    "harbor.guardianintelligence.org"    = local.public_ingress_ipv4s
    "keycloak.guardianintelligence.org"  = local.public_ingress_ipv4s
    "s3.guardianintelligence.org"        = local.public_ingress_ipv4s
  }

  cloudflare_record_names = {
    for hostname in keys(local.cloudflare_record_sets) :
    hostname => hostname == local.cloudflare_zone_name ? "@" : trimsuffix(hostname, ".${local.cloudflare_zone_name}")
  }

  cloudflare_a_records = merge([
    for hostname, addresses in local.cloudflare_record_sets : {
      for address in addresses : "${hostname}/${address}" => {
        hostname = hostname
        address  = address
      }
    }
  ]...)
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

data "aws_route53_zone" "gi_org" {
  name         = local.route53_zone_name
  private_zone = false
}

data "cloudflare_zone" "guardianintelligence_org" {
  name       = local.cloudflare_zone_name
  account_id = var.cloudflare_account_id
}

resource "aws_route53_record" "gi_org_a" {
  for_each = local.route53_record_sets

  zone_id         = data.aws_route53_zone.gi_org.zone_id
  name            = each.key
  type            = "A"
  ttl             = 60
  records         = each.value
  allow_overwrite = true
}

resource "cloudflare_record" "guardianintelligence_org_a" {
  for_each = local.cloudflare_a_records

  zone_id         = data.cloudflare_zone.guardianintelligence_org.id
  name            = local.cloudflare_record_names[each.value.hostname]
  type            = "A"
  value           = each.value.address
  ttl             = 60
  proxied         = false
  allow_overwrite = true
}

check "no_legacy_verself_records" {
  assert {
    condition = alltrue([
      for addresses in concat(values(local.route53_record_sets), values(local.cloudflare_record_sets)) :
      !contains(addresses, "206.223.228.99") && !contains(addresses, "67.213.115.113")
    ])
    error_message = "Public DNS records must not point at excluded Verself prod or the retired ash-bm-003 address."
  }
}
