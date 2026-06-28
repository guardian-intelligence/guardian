provider "vault" {
  address = var.openbao_addr
}

locals {
  kubernetes_auth_mount = "kubernetes"
  kv_mount              = "kv"
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

resource "vault_policy" "ops_controller" {
  name = "guardian-openbao-ops-controller"

  policy = <<-EOT
    path "sys/policies/acl/guardian-*" {
      capabilities = ["create", "read", "update", "delete"]
    }

    path "auth/${local.kubernetes_auth_mount}/role/guardian-*" {
      capabilities = ["create", "read", "update", "delete"]
    }

    path "sys/auth/${local.kubernetes_auth_mount}" {
      capabilities = ["create", "read", "update", "delete"]
    }

    path "sys/auth/${local.kubernetes_auth_mount}/tune" {
      capabilities = ["read", "update"]
    }

    path "sys/mounts/${local.kv_mount}" {
      capabilities = ["create", "read", "update", "delete"]
    }

    path "sys/mounts/${local.kv_mount}/tune" {
      capabilities = ["read", "update"]
    }
  EOT
}

resource "vault_kubernetes_auth_backend_role" "ops_controller" {
  backend                          = vault_auth_backend.kubernetes.path
  role_name                        = "guardian-openbao-ops-controller"
  bound_service_account_names      = ["openbao-ops-controller"]
  bound_service_account_namespaces = ["tenant-guardian"]
  audience                         = "openbao"
  token_policies                   = [vault_policy.ops_controller.name]
  token_ttl                        = 900
  token_max_ttl                    = 3600
}

removed {
  from = vault_mount.kv

  lifecycle {
    destroy = false
  }
}

removed {
  from = vault_policy.external_dns

  lifecycle {
    destroy = false
  }
}

removed {
  from = vault_kubernetes_auth_backend_role.external_dns

  lifecycle {
    destroy = false
  }
}
