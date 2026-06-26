output "project_id" {
  value = local.project_id
}

output "virtual_network" {
  value = {
    id  = latitudesh_virtual_network.management.id
    vid = latitudesh_virtual_network.management.vid
  }
}

output "management_vlan" {
  value = {
    id                  = latitudesh_virtual_network.management.id
    vid                 = latitudesh_virtual_network.management.vid
    description         = latitudesh_virtual_network.management.description
    subnet              = local.vlan.subnet
    vlan_mtu            = local.vlan.vlan_mtu
    pod_mtu             = local.vlan.pod_mtu
    api_vip             = local.vlan.api_vip
    api_server_endpoint = "https://${local.vlan.api_vip}:6443"
    vip_link            = local.vlan.vip_link
    metallb_pool        = local.vlan.metallb_pool
  }
}

output "api_vip" {
  value = local.vlan.api_vip
}

output "vip_link" {
  value = local.vlan.vip_link
}

output "control_plane_public_ipv4" {
  value = {
    for name, server in latitudesh_server.control_plane :
    name => server.primary_ipv4
  }
}

output "control_plane_private_ipv4" {
  value = {
    for name, node in local.control_plane_nodes :
    name => node.private_ipv4
  }
}

output "control_plane_nodes" {
  value = {
    for name, node in local.control_plane_nodes :
    name => {
      server_id    = latitudesh_server.control_plane[name].id
      hostname     = latitudesh_server.control_plane[name].hostname
      public_ipv4  = latitudesh_server.control_plane[name].primary_ipv4
      private_ipv4 = node.private_ipv4
    }
  }
}
