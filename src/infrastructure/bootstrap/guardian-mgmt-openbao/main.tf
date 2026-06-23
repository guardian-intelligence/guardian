provider "vault" {
  address = var.openbao_addr
}

locals {
  kubernetes_auth_mount = "kubernetes"
  kv_mount              = "kv"
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
