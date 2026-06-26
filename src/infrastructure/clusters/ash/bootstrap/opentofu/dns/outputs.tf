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
