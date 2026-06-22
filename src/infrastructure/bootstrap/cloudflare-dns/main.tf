locals {
  inventory              = jsondecode(file("${path.module}/../../inventory/guardian-mgmt.json"))
  publishing             = local.inventory.publishing
  management_public_ipv4 = [for node in local.inventory.nodes : node.public_ipv4]

  cloudflare_zone_id = local.publishing.cloudflare.zone_id
  managed_a_records = [
    for record in local.publishing.dns_records : record
    if record.type == "A" && record.targets == "management_public_ipv4"
  ]
  public_a_records = flatten([
    for record in local.managed_a_records : [
      for ip in local.management_public_ipv4 : {
        name    = record.name
        type    = record.type
        content = ip
        proxied = record.proxied
        ttl     = record.ttl
      }
    ]
  ])
}

resource "cloudflare_dns_record" "public_ingress_a" {
  for_each = {
    for record in local.public_a_records :
    "${record.name}/${record.content}" => record
  }

  zone_id = local.cloudflare_zone_id
  name    = each.value.name
  type    = each.value.type
  content = each.value.content
  proxied = each.value.proxied
  ttl     = each.value.ttl
}

check "public_ingress_a_records_are_public" {
  assert {
    condition = alltrue([
      for record in local.public_a_records :
      !startswith(record.content, "10.")
    ])
    error_message = "Public Cloudflare A records must not point at the private Latitude VLAN range."
  }
}

check "management_public_ipv4_are_unique" {
  assert {
    condition     = length(distinct(local.management_public_ipv4)) == length(local.management_public_ipv4)
    error_message = "Management node public IPv4 addresses must be unique before deriving Cloudflare A records."
  }
}
