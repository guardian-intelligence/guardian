# Lane roots consume their credential straight from this root's output at
# apply time, e.g.:
#   CLOUDFLARE_API_TOKEN=$(tofu -chdir=../guardian-mgmt-cloudflare-tokens output -raw dns_lb_provisioner_token_value)

output "dns_lb_provisioner_token_id" {
  description = "Token id of the dns-lb provisioner lane token."
  value       = cloudflare_account_token.dns_lb_provisioner.id
}

output "dns_lb_provisioner_token_value" {
  description = "Secret value of the dns-lb provisioner lane token."
  value       = cloudflare_account_token.dns_lb_provisioner.value
  sensitive   = true
}

output "external_dns_token_id" {
  description = "Token id of the external-dns lane token."
  value       = cloudflare_account_token.external_dns.id
}

output "external_dns_token_value" {
  description = "Secret value of the external-dns lane token (relayed into OpenBao, never consumed directly)."
  value       = cloudflare_account_token.external_dns.value
  sensitive   = true
}

output "edge_policy_provisioner_token_id" {
  description = "Token id of the edge-policy provisioner lane token."
  value       = cloudflare_account_token.edge_policy_provisioner.id
}

output "edge_policy_provisioner_token_value" {
  description = "Secret value of the edge-policy provisioner lane token."
  value       = cloudflare_account_token.edge_policy_provisioner.value
  sensitive   = true
}

output "r2_backups_access_key_id" {
  description = "S3 access key id for the guardian-backups bucket (the R2 token's id)."
  value       = cloudflare_account_token.r2_backups.id
}

output "r2_backups_secret_access_key" {
  description = "S3 secret access key for the guardian-backups bucket (SHA-256 of the R2 token value)."
  value       = sha256(cloudflare_account_token.r2_backups.value)
  sensitive   = true
}

output "r2_state_access_key_id" {
  description = "S3 access key id for the guardian-vault state bucket (custody-mirrored: cloudflare_r2_access_key_id)."
  value       = cloudflare_account_token.r2_state.id
}

output "r2_state_secret_access_key" {
  description = "S3 secret access key for the guardian-vault state bucket (custody-mirrored: cloudflare_r2_secret_access_key)."
  value       = sha256(cloudflare_account_token.r2_state.value)
  sensitive   = true
}
