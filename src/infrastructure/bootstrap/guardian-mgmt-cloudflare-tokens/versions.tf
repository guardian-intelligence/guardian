terraform {
  required_version = ">= 1.12.0"

  required_providers {
    cloudflare = {
      # v5 is required for cloudflare_account_token (account-owned tokens).
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

  # Applies run with CLOUDFLARE_API_TOKEN set from the custody
  # cloudflare_token_minter_api_token key. Token minting requires Account API
  # Tokens Read/Write + Account Settings Read; bucket-owning applies also
  # require Workers R2 Storage Write. This root-equivalent credential remains
  # custody-only and never enters the cluster.
  # This state holds every lane token VALUE so consumers can be re-seeded at
  # DR time: it is the most sensitive state in the guardian-vault bucket, and
  # the encryption block above is what keeps those values ciphertext there —
  # bucket credentials alone read nothing.
  backend "s3" {
    bucket = "guardian-vault"
    key    = "opentofu/guardian-mgmt-cloudflare-tokens.tfstate"
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
