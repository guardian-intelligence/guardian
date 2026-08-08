output "cloudflare_zone_id" {
  description = "Cloudflare zone id used by ExternalDNS zone-id-filter."
  value       = data.cloudflare_zone.guardianintelligence_org.id
}

output "external_dns_owner_id" {
  description = "TXT owner id used by the in-cluster ExternalDNS controller."
  value       = local.external_dns_owner_id
}

output "cloudflare_load_balancer_hostnames" {
  description = "Root public edge hostnames actively served by Cloudflare Load Balancing."
  value       = local.public_edge_hostnames
}

output "cloudflare_load_balancer_pool_id" {
  description = "Cloudflare Load Balancer pool id for guardian-mgmt ASH public origins."
  value       = cloudflare_load_balancer_pool.guardian_mgmt_ash.id
}

output "public_ingress_ipv4s" {
  description = "Latitude public ingress origin IPs published behind Cloudflare Load Balancing."
  value       = local.public_ingress_ipv4s
}

output "rumi_engineering_zone_id" {
  description = "Cloudflare zone id for the rumi.engineering product edge."
  value       = data.cloudflare_zone.rumi_engineering.id
}

output "codex_cloud_tunnel_token" {
  description = "Connector token relayed to OpenBao for the in-cluster cloudflared Deployment."
  value       = data.cloudflare_zero_trust_tunnel_cloudflared_token.guardian_codex_cloud.token
  sensitive   = true
}

output "codex_cloud_access_client_id" {
  description = "Cloudflare Access service-token client id installed only in the Codex cloud environment."
  value       = cloudflare_zero_trust_access_service_token.guardian_codex_cloud.client_id
}

output "codex_cloud_access_client_secret" {
  description = "Cloudflare Access service-token secret installed only in the Codex cloud environment."
  value       = cloudflare_zero_trust_access_service_token.guardian_codex_cloud.client_secret
  sensitive   = true
}

output "cloud_agent_access_client_ids" {
  description = "Provider-specific Cloudflare Access transport client ids."
  value = {
    for provider, token in cloudflare_zero_trust_access_service_token.guardian_cloud_agent :
    provider => token.client_id
  }
}

output "cloud_agent_access_client_secrets" {
  description = "Provider-specific Cloudflare Access transport secrets installed only in the matching provider."
  value = {
    for provider, token in cloudflare_zero_trust_access_service_token.guardian_cloud_agent :
    provider => token.client_secret
  }
  sensitive = true
}
