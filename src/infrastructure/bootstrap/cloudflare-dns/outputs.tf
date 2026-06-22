output "managed_public_a_records" {
  value = {
    for key, record in cloudflare_dns_record.public_ingress_a :
    key => {
      name    = record.name
      content = record.content
      proxied = record.proxied
      ttl     = record.ttl
    }
  }
}

output "management_public_ipv4" {
  value = local.management_public_ipv4
}
