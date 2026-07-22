terraform {
  required_version = ">= 1.12.0"

  required_providers {
    cloudflare = {
      # v5 is required for cloudflare_account_token (account-owned tokens).
      source  = "cloudflare/cloudflare"
      version = "= 5.21.1"
    }
  }

  # State encryption: the R2 bucket holds ciphertext, custody holds the key.
  # The pbkdf2 passphrase is the custody.env `tofu_state_encryption_passphrase`
  # value, merged in at run time through TF_ENCRYPTION (cold-boot-bootstrap.md,
  # "OpenTofu state encryption") — never a *.tf literal, never a tofu variable.
  # The unencrypted fallback only reads state written before the encryption
  # ceremony; once every root's state is confirmed encrypted, a follow-up PR
  # deletes the fallback and sets `enforced = true` on the state block.
  encryption {
    key_provider "pbkdf2" "custody" {}

    method "aes_gcm" "custody" {
      keys = key_provider.pbkdf2.custody
    }

    method "unencrypted" "migration" {}

    state {
      method = method.aes_gcm.custody

      fallback {
        method = method.unencrypted.migration
      }
    }
  }

  # Applies run with CLOUDFLARE_API_TOKEN set from the custody
  # cloudflare_token_minter_api_token key (Account API Tokens Read/Write +
  # Account Settings Read — root-equivalent, custody-only, never in-cluster).
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
    use_path_style              = true
  }
}

provider "cloudflare" {}
