provider "vault" {
  address = var.openbao_addr
}

locals {
  kubernetes_auth_mount = "kubernetes"
  kv_mount              = "kv"

  secret_projections = {
    tenant-root-cnpg-backup = {
      service_account = "guardian-external-secrets"
      namespace       = "tenant-root"
      path            = "guardian/guardian-mgmt/tenant-root/postgres/guardian/cnpg-backup"
    }
    tenant-guardiancommercial-platform-dev-cnpg-backup = {
      service_account = "guardian-external-secrets"
      namespace       = "tenant-guardiancommercial-platform-dev"
      path            = "guardian/guardian-mgmt/tenant-guardiancommercial-platform-dev/postgres/guardian/cnpg-backup"
    }
    tenant-guardiancommercial-platform-gamma-cnpg-backup = {
      service_account = "guardian-external-secrets"
      namespace       = "tenant-guardiancommercial-platform-gamma"
      path            = "guardian/guardian-mgmt/tenant-guardiancommercial-platform-gamma/postgres/guardian/cnpg-backup"
    }
    tenant-guardiancommercial-platform-prod-cnpg-backup = {
      service_account = "guardian-external-secrets"
      namespace       = "tenant-guardiancommercial-platform-prod"
      path            = "guardian/guardian-mgmt/tenant-guardiancommercial-platform-prod/postgres/guardian/cnpg-backup"
    }
    tenant-root-clickhouse-backup = {
      service_account = "guardian-clickhouse-external-secrets"
      namespace       = "tenant-root"
      path            = "guardian/guardian-mgmt/tenant-root/clickhouse/guardian/backup"
    }
    tenant-guardiancommercial-platform-dev-clickhouse-backup = {
      service_account = "guardian-clickhouse-external-secrets"
      namespace       = "tenant-guardiancommercial-platform-dev"
      path            = "guardian/guardian-mgmt/tenant-guardiancommercial-platform-dev/clickhouse/guardian/backup"
    }
    tenant-guardiancommercial-platform-gamma-clickhouse-backup = {
      service_account = "guardian-clickhouse-external-secrets"
      namespace       = "tenant-guardiancommercial-platform-gamma"
      path            = "guardian/guardian-mgmt/tenant-guardiancommercial-platform-gamma/clickhouse/guardian/backup"
    }
    tenant-guardiancommercial-platform-prod-clickhouse-backup = {
      service_account = "guardian-clickhouse-external-secrets"
      namespace       = "tenant-guardiancommercial-platform-prod"
      path            = "guardian/guardian-mgmt/tenant-guardiancommercial-platform-prod/clickhouse/guardian/backup"
    }
  }
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

resource "vault_policy" "secret_projection" {
  for_each = local.secret_projections

  name = each.key

  policy = <<-EOT
path "${vault_mount.kv.path}/data/${each.value.path}" {
  capabilities = ["read"]
}

path "${vault_mount.kv.path}/metadata/${each.value.path}" {
  capabilities = ["read"]
}
EOT
}

resource "vault_kubernetes_auth_backend_role" "secret_projection" {
  for_each = local.secret_projections

  backend                          = vault_auth_backend.kubernetes.path
  role_name                        = each.key
  bound_service_account_names      = [each.value.service_account]
  bound_service_account_namespaces = [each.value.namespace]
  audience                         = "openbao"
  token_policies                   = [vault_policy.secret_projection[each.key].name]
  token_ttl                        = 3600
  token_no_default_policy          = true
}
