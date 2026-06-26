variable "openbao_addr" {
  description = "OpenBao API address. The default targets the tenant-scoped Guardian KMS authority; operators can override it when using a local port-forward."
  type        = string
  default     = "http://openbao-guardian.tenant-guardian-kms.svc:8200"
}
