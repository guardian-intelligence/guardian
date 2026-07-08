terraform {
  required_version = ">= 1.12.0"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "= 4.52.5"
    }
  }

  # Same R2 backend pattern as guardian-mgmt-dns, separate state key. Edge
  # policy (AOP, cache rules, bot management) is deliberately a different
  # tofu root than DNS/LB: dns-lb-provisioner stays a minimal DR actor whose
  # empty plan is a drift oracle, and the edge-policy-provisioner token
  # cannot move traffic. Applies run with CLOUDFLARE_API_TOKEN read from the
  # guardian-mgmt-cloudflare-tokens root:
  #   tofu -chdir=../guardian-mgmt-cloudflare-tokens output -raw edge_policy_provisioner_token_value
  backend "s3" {
    bucket = "guardian-vault"
    key    = "opentofu/guardian-mgmt-edge-policy.tfstate"
    region = "auto"

    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_path_style              = true
  }
}

provider "cloudflare" {}
