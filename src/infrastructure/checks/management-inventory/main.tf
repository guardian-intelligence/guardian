locals {
  inventory = jsondecode(file("${path.module}/../../inventory/guardian-mgmt.json"))
  vlan      = local.inventory.network.vlan

  node_public_ipv4  = [for node in local.inventory.nodes : node.public_ipv4]
  node_private_ipv4 = [for node in local.inventory.nodes : node.private_ipv4]
  api_endpoint      = "https://${local.vlan.api_vip}:6443"

  metallb_docs = [
    for raw in split("\n---\n", file("${path.module}/../../base/networking/metallb.yaml")) :
    yamldecode(raw)
    if trimspace(raw) != ""
  ]
  metallb_pool_addresses = one([
    for doc in local.metallb_docs : doc.spec.addresses
    if doc.kind == "IPAddressPool" && doc.metadata.name == "cozystack"
  ])

  subnet_docs = [
    for raw in split("\n---\n", file("${path.module}/../../base/networking/subnet-mtu.yaml")) :
    yamldecode(raw)
    if trimspace(raw) != ""
  ]
  subnet_mtu_values = distinct([
    for doc in local.subnet_docs : doc.spec.mtu
  ])

  cozystack_platform = yamldecode(file("${path.module}/../../base/cozystack/platform.yaml"))
  cozystack_values   = local.cozystack_platform.spec.components.platform.values

  talm_values    = yamldecode(file("${path.module}/../../talm/values.yaml"))
  talm_cert_sans = [for san in local.talm_values.certSANs : tostring(san)]
}

check "node_public_ipv4_are_unique" {
  assert {
    condition     = length(distinct(local.node_public_ipv4)) == length(local.node_public_ipv4)
    error_message = "Management node public IPv4 addresses must be unique."
  }
}

check "node_private_ipv4_are_unique" {
  assert {
    condition     = length(distinct(local.node_private_ipv4)) == length(local.node_private_ipv4)
    error_message = "Management node private IPv4 addresses must be unique."
  }
}

check "cozystack_api_endpoint_matches_inventory_vip" {
  assert {
    condition     = local.cozystack_values.publishing.apiServerEndpoint == local.api_endpoint
    error_message = "Cozystack platform apiServerEndpoint must match the inventory API VIP."
  }
}

check "cozystack_external_ips_match_inventory_nodes" {
  assert {
    condition     = local.cozystack_values.publishing.externalIPs == local.node_public_ipv4
    error_message = "Cozystack platform externalIPs must match inventory nodes[*].public_ipv4."
  }
}

check "metallb_pool_matches_inventory" {
  assert {
    condition     = local.metallb_pool_addresses == [local.vlan.metallb_pool]
    error_message = "MetalLB address pool must match inventory.network.vlan.metallb_pool."
  }
}

check "subnet_mtu_matches_inventory" {
  assert {
    condition     = length(local.subnet_mtu_values) == 1 && local.subnet_mtu_values[0] == local.vlan.pod_mtu
    error_message = "All kube-ovn Subnet MTUs must match inventory.network.vlan.pod_mtu."
  }
}

check "talm_endpoint_matches_inventory_vip" {
  assert {
    condition     = local.talm_values.endpoint == local.api_endpoint
    error_message = "Talm endpoint must match the inventory API VIP."
  }
}

check "talm_floating_ip_matches_inventory_vip" {
  assert {
    condition     = local.talm_values.floatingIP == local.vlan.api_vip
    error_message = "Talm floatingIP must match inventory.network.vlan.api_vip."
  }
}

check "talm_vip_link_matches_inventory" {
  assert {
    condition     = local.talm_values.vipLink == local.vlan.vip_link
    error_message = "Talm vipLink must match inventory.network.vlan.vip_link."
  }
}

check "talm_advertised_subnets_match_inventory" {
  assert {
    condition     = local.talm_values.advertisedSubnets == [local.vlan.subnet]
    error_message = "Talm advertisedSubnets must match inventory.network.vlan.subnet."
  }
}

check "talm_cert_sans_cover_inventory_endpoints" {
  assert {
    condition = length(setsubtract(
      toset(concat([local.vlan.api_vip], local.node_public_ipv4)),
      toset(local.talm_cert_sans),
    )) == 0
    error_message = "Talm certSANs must include the inventory API VIP and every management node public IPv4."
  }
}
