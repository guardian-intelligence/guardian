terraform {
  required_version = ">= 1.12.0"

  required_providers {
    vault = {
      source  = "hashicorp/vault"
      version = "= 4.4.0"
    }
  }

  # R2 is S3-compatible. Credentials and the R2 endpoint come from AWS_*
  # environment variables during `tofu init`.
  backend "s3" {
    bucket = "guardian-vault"
    key    = "opentofu/guardian-mgmt-openbao.tfstate"
    region = "auto"

    skip_credentials_validation = true
    skip_metadata_api_check     = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    use_path_style              = true
  }
}
