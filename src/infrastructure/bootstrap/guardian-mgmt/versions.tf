terraform {
  required_version = ">= 1.12.0"

  required_providers {
    latitudesh = {
      source  = "latitudesh/latitudesh"
      version = "= 3.3.0"
    }
  }

  # R2 is S3-compatible. The bucket/key are declarative; the endpoint is derived
  # from the shared Cloudflare account id tfvars during `tofu init`; credentials
  # still come from standard AWS_* environment variables.
  backend "s3" {
    bucket   = "guardian-vault"
    endpoint = "https://${var.cloudflare_account_id}.r2.cloudflarestorage.com"
    key      = "opentofu/guardian-mgmt.tfstate"
    region   = "auto"

    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_path_style              = true
  }
}

provider "latitudesh" {}
