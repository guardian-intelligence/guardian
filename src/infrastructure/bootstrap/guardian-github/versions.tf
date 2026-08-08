terraform {
  required_version = ">= 1.12.0"

  required_providers {
    github = {
      source  = "integrations/github"
      version = "6.13.0"
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

  # No credential material is created here — this root only describes
  # repository and branch policy — so the state lives with the other
  # non-custody roots in guardian-vault.
  backend "s3" {
    bucket = "guardian-vault"
    key    = "opentofu/guardian-github.tfstate"
    region = "auto"

    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_lockfile                = true
    use_path_style              = true
  }
}

# Two per-org fine-grained PATs — fine-grained PATs are scoped to a single
# resource owner, so each organization gets its own (see the runbook for the
# exact permission set). The guardian-intelligence token rides GITHUB_TOKEN;
# the provider reads only that one env var, so the customer-org token
# arrives as a sensitive variable through TF_VAR_github_customer_token.
# Provider configuration is not persisted, so neither token lands in state.
provider "github" {
  owner = "guardian-intelligence"
}

# The simulated customer fleet lives in a second organization on purpose: a
# customer that shares an org with the vendor is not a customer.
provider "github" {
  alias = "customer"
  owner = "digital-guardian-software"
  token = var.github_customer_token
}
