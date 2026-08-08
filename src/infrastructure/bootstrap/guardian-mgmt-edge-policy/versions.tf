terraform {
  required_version = ">= 1.12.0"

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "= 5.21.1"
    }
  }

  # State encryption: the R2 bucket holds ciphertext; the pbkdf2 passphrase
  # lives in OpenBao (tofu-system/state-encryption, carried into runner env
  # by ESO) with a disaster-recovery twin in the operator vault. Merged in at
  # run time through TF_ENCRYPTION — never a *.tf literal, never a tofu
  # variable (docs/tofu-gitops-design.md).
  encryption {
    key_provider "pbkdf2" "state" {}

    method "aes_gcm" "state" {
      keys = key_provider.pbkdf2.state
    }

    state {
      method   = method.aes_gcm.state
      enforced = true
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
    use_lockfile                = true
    use_path_style              = true
  }
}

provider "cloudflare" {}
