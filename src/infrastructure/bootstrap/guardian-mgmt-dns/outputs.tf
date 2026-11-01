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

output "anveio_com_zone_id" {
  description = "Cloudflare zone id for the anveio.com contact card."
  value       = data.cloudflare_zone.anveio_com.id
}

output "codex_cloud_tunnel_token" {
  description = "Connector token relayed to OpenBao for the in-cluster cloudflared Deployment."
  value       = data.cloudflare_zero_trust_tunnel_cloudflared_token.guardian_codex_cloud.token
  sensitive   = true
}

output "operator_mac_access_credential" {
  description = "Transport-only credential relayed through stdin into this Mac's login Keychain."
  value = {
    client_id     = cloudflare_zero_trust_access_service_token.guardian_operator_mac.client_id
    client_secret = cloudflare_zero_trust_access_service_token.guardian_operator_mac.client_secret
    expires_at    = cloudflare_zero_trust_access_service_token.guardian_operator_mac.expires_at
  }
  sensitive = true
}
