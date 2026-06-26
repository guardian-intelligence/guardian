provider "vault" {
  address = var.openbao_addr
}

locals {
  kubernetes_auth_mount      = "kubernetes"
  kv_mount                   = "kv"
  transit_mount              = "transit"
  external_dns_secret        = "guardian/guardian-mgmt/tenant-root/dns/external-dns"
  third_party_secret_prefix  = "guardian/guardian-mgmt/integrations"
  third_party_encryption_key = "guardian-integrations-encryption"
  third_party_signing_key    = "guardian-integrations-signing"
}

resource "vault_mount" "kv" {
  path        = local.kv_mount
  type        = "kv-v2"
  description = "Guardian management cluster secret material."

  options = {
    version = "2"
    type    = "kv-v2"
  }
}

resource "vault_mount" "transit" {
  path        = local.transit_mount
  type        = "transit"
  description = "Guardian transit authority for third-party integration signing, HMAC, and envelope encryption."
}

resource "vault_transit_secret_backend_key" "third_party_encryption" {
  backend          = vault_mount.transit.path
  name             = local.third_party_encryption_key
  type             = "aes256-gcm96"
  deletion_allowed = false
  exportable       = false
}

resource "vault_transit_secret_backend_key" "third_party_signing" {
  backend          = vault_mount.transit.path
  name             = local.third_party_signing_key
  type             = "ed25519"
  deletion_allowed = false
  exportable       = false
}

resource "vault_auth_backend" "kubernetes" {
  path        = local.kubernetes_auth_mount
  type        = "kubernetes"
  description = "Kubernetes service account auth for guardian-mgmt workloads."
}

resource "vault_kubernetes_auth_backend_config" "guardian_mgmt" {
  backend                = vault_auth_backend.kubernetes.path
  kubernetes_host        = "https://kubernetes.default.svc:443"
  disable_iss_validation = true
}

resource "vault_policy" "external_dns" {
  name = "tenant-root-external-dns"

  policy = <<-EOT
    path "${local.kv_mount}/data/${local.external_dns_secret}" {
      capabilities = ["read"]
    }

    path "${local.kv_mount}/metadata/${local.external_dns_secret}" {
      capabilities = ["read"]
    }
  EOT
}

resource "vault_policy" "third_party_secret_reader" {
  name = "guardian-third-party-secret-reader"

  policy = <<-EOT
    path "${local.kv_mount}/data/${local.third_party_secret_prefix}/*" {
      capabilities = ["read"]
    }

    path "${local.kv_mount}/metadata/${local.third_party_secret_prefix}/*" {
      capabilities = ["read"]
    }
  EOT
}

resource "vault_policy" "third_party_transit_client" {
  name = "guardian-third-party-transit-client"

  policy = <<-EOT
    path "${local.transit_mount}/encrypt/${local.third_party_encryption_key}" {
      capabilities = ["update"]
    }

    path "${local.transit_mount}/decrypt/${local.third_party_encryption_key}" {
      capabilities = ["update"]
    }

    path "${local.transit_mount}/rewrap/${local.third_party_encryption_key}" {
      capabilities = ["update"]
    }

    path "${local.transit_mount}/datakey/plaintext/${local.third_party_encryption_key}" {
      capabilities = ["update"]
    }

    path "${local.transit_mount}/hmac/${local.third_party_encryption_key}" {
      capabilities = ["update"]
    }

    path "${local.transit_mount}/verify/${local.third_party_encryption_key}" {
      capabilities = ["update"]
    }

    path "${local.transit_mount}/sign/${local.third_party_signing_key}" {
      capabilities = ["update"]
    }

    path "${local.transit_mount}/verify/${local.third_party_signing_key}" {
      capabilities = ["update"]
    }
  EOT
}

resource "vault_kubernetes_auth_backend_role" "external_dns" {
  backend                          = vault_auth_backend.kubernetes.path
  role_name                        = "tenant-root-external-dns"
  bound_service_account_names      = ["external-dns-secrets"]
  bound_service_account_namespaces = ["external-dns"]
  audience                         = "openbao"
  token_policies                   = [vault_policy.external_dns.name]
  token_ttl                        = 3600
  token_max_ttl                    = 3600
}

resource "vault_kubernetes_auth_backend_role" "github_integrations" {
  backend                          = vault_auth_backend.kubernetes.path
  role_name                        = "guardian-github-integrations"
  bound_service_account_names      = ["github-actions-runner-controller", "github-app-secrets"]
  bound_service_account_namespaces = ["arc-systems", "guardian-release"]
  audience                         = "openbao"
  token_policies = [
    vault_policy.third_party_secret_reader.name,
    vault_policy.third_party_transit_client.name,
  ]
  token_ttl     = 3600
  token_max_ttl = 3600
}
