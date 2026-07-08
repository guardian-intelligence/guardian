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
