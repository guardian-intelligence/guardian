terraform {
  required_version = ">= 1.12.0"

  required_providers {
    latitudesh = {
      source  = "latitudesh/latitudesh"
      version = "3.5.3"
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

  # R2 is S3-compatible. The bucket/key are declarative; the endpoint is supplied
  # as partial backend config during `tofu init` after deriving it from the
  # shared Cloudflare account id file. Credentials still come from standard
  # AWS_* environment variables.
  backend "s3" {
    bucket = "guardian-vault"
    key    = "opentofu/guardian-mgmt.tfstate"
    region = "auto"

    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_lockfile                = true
    use_path_style              = true
  }
}

provider "latitudesh" {}
