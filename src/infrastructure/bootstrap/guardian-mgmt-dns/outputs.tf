output "route53_records" {
  description = "Route53 A records managed for gi.org."
  value       = local.route53_record_sets
}

output "cloudflare_records" {
  description = "Cloudflare A records managed for guardianintelligence.org."
  value       = local.cloudflare_record_sets
}
