output "project_id" {
  value = latitudesh_project.guardian_mgmt.id
}

output "virtual_network" {
  value = {
    id  = latitudesh_virtual_network.management.id
    vid = latitudesh_virtual_network.management.vid
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
