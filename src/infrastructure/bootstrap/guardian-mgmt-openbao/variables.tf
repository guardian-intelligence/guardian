variable "openbao_addr" {
  description = "OpenBao API address. The default is reachable from inside the management cluster; operators can override it when using a local port-forward."
  type        = string
  default     = "http://guardian-openbao.tenant-guardian.svc:8200"
}
