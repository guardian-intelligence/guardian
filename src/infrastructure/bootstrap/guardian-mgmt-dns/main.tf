locals {
  cloudflare_zone_name  = "guardianintelligence.org"
  public_ingress_node   = "ash-earth"
  external_dns_owner_id = "guardian-mgmt-ash"
  external_dns_record_hostnames = [
    "*.guardianintelligence.org",
    "guardianintelligence.org",
    "api.guardianintelligence.org",
    "dashboard.guardianintelligence.org",
    "grafana.guardianintelligence.org",
    "harbor.guardianintelligence.org",
    "keycloak.guardianintelligence.org",
    "s3.guardianintelligence.org",
  ]

  public_ingress_ipv4s = [
    data.terraform_remote_state.guardian_mgmt.outputs.control_plane_nodes[local.public_ingress_node].public_ipv4
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

check "external_dns_owns_dns_records" {
  assert {
    condition     = length(local.external_dns_record_hostnames) == 8
    error_message = "Root public DNS record ownership belongs to the in-cluster ExternalDNS controller."
  }
}
