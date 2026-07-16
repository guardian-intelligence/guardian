terraform {
  required_version = ">= 1.12.0"

  required_providers {
    cloudflare = {
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

  # R2 is S3-compatible. The bucket/key are declarative; the endpoint is supplied
  # as partial backend config during `tofu init` after deriving it from the
  # shared Cloudflare account id file. Credentials still come from standard
  # AWS_* environment variables.
  backend "s3" {
    bucket = "guardian-vault"
    key    = "opentofu/guardian-mgmt-dns.tfstate"
    region = "auto"

    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_path_style              = true
  }
}

provider "cloudflare" {}
