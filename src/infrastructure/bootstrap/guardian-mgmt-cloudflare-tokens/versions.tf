terraform {
  required_version = ">= 1.12.0"

  required_providers {
    cloudflare = {
      # v5 is required for cloudflare_account_token (account-owned tokens);
      # the dns and edge-policy roots stay on 4.52.5 — roots pin providers
      # independently.
      source  = "cloudflare/cloudflare"
      version = "= 5.21.1"
    }
  }

  # Applies run with CLOUDFLARE_API_TOKEN set from the custody
  # cloudflare_token_minter_api_token key (Account API Tokens Read/Write +
  # Account Settings Read — root-equivalent, custody-only, never in-cluster).
  # This state file holds every lane token VALUE in cleartext: it is the most
  # sensitive object in the guardian-vault bucket by design — the values must
  # live somewhere retrievable so consumers can be re-seeded at DR time, and
  # anyone holding the bucket credentials already holds custody-equivalent
  # access.
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
