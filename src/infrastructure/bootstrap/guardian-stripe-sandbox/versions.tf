terraform {
  required_version = ">= 1.12.0"

  required_providers {
    stripe = {
      source  = "stripe/stripe"
      version = "0.2.3"
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
