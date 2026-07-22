terraform {
  required_version = ">= 1.12.0"

  required_providers {
    stripe = {
      source  = "stripe/stripe"
      version = "0.2.3"
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

  # The webhook signing secret is creation-only and is therefore retained in
  # this state. guardian-vault is credential-bearing custody infrastructure;
  # access to this state is equivalent to access to the sandbox webhook.
  backend "s3" {
    bucket = "guardian-vault"
    key    = "opentofu/guardian-stripe-sandbox.tfstate"
    region = "auto"

    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_path_style              = true
  }
}

provider "stripe" {}
