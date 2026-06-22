variable "cloudflare_account_id" {
  description = "Cloudflare account id used to derive the R2 S3-compatible OpenTofu backend endpoint."
  type        = string
}

variable "openbao_addr" {
  description = "OpenBao API address. The default is reachable from inside the management cluster; operators can override it when using a local port-forward."
  type        = string
  default     = "http://openbao-guardian.tenant-root.svc:8200"
}
