provider "vault" {
  address = var.openbao_addr
}

locals {
  kubernetes_auth_mount = "kubernetes"
  kv_mount              = "kv"
  external_dns_secret   = "guardian/guardian-mgmt/tenant-guardian/dns/external-dns"
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
  name = "guardian-external-dns"

  policy = <<-EOT
    path "${local.kv_mount}/data/${local.external_dns_secret}" {
      capabilities = ["read"]
    }

    path "${local.kv_mount}/metadata/${local.external_dns_secret}" {
      capabilities = ["read"]
    }
  EOT
}

resource "vault_kubernetes_auth_backend_role" "external_dns" {
  backend                          = vault_auth_backend.kubernetes.path
  role_name                        = "guardian-external-dns"
  bound_service_account_names      = ["external-dns-secrets"]
  bound_service_account_namespaces = ["external-dns"]
  audience                         = "openbao"
  token_policies                   = [vault_policy.external_dns.name]
  token_ttl                        = 3600
  token_max_ttl                    = 3600
}
