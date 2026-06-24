output "cloudflare_zone_id" {
  description = "Cloudflare zone id used by ExternalDNS zone-id-filter."
  value       = data.cloudflare_zone.guardianintelligence_org.id
}

output "external_dns_owner_id" {
  description = "TXT owner id used by the in-cluster ExternalDNS controller."
  value       = local.external_dns_owner_id
}

output "external_dns_record_hostnames" {
  description = "Root public DNS hostnames reconciled by ExternalDNS."
  value       = local.external_dns_record_hostnames
}

output "public_ingress_ipv4s" {
  description = "Public ingress IPs published by ExternalDNS DNSEndpoints."
  value       = local.public_ingress_ipv4s
}
