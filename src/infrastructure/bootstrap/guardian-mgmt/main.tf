data "latitudesh_region" "ash" {
  slug = local.site
}

data "latitudesh_plan" "f4_metal_small" {
  name = "f4.metal.small"
}

locals {
  inventory = jsondecode(file("${path.module}/../../inventory/guardian-mgmt.json"))

  project_id = local.inventory.latitude.project_id
  site       = local.inventory.latitude.site
  vlan       = local.inventory.network.vlan
  control_plane_nodes = {
    for node in local.inventory.nodes : node.name => node
  }
}

resource "latitudesh_project" "guardian_mgmt" {
  name              = "guardian-mgmt"
  environment       = "Production"
  provisioning_type = "on_demand"
  description       = "Guardian management control plane"

  lifecycle {
    prevent_destroy = true
  }
}

resource "latitudesh_virtual_network" "management" {
  description = local.vlan.description
  project     = latitudesh_project.guardian_mgmt.id
  site        = data.latitudesh_region.ash.slug

  lifecycle {
    prevent_destroy = true
  }
}

resource "latitudesh_server" "control_plane" {
  for_each = local.control_plane_nodes

  hostname         = each.value.hostname
  plan             = data.latitudesh_plan.f4_metal_small.slug
  site             = data.latitudesh_region.ash.slug
  project          = latitudesh_project.guardian_mgmt.id
  operating_system = "ubuntu_24_04_x64_lts"

  # Adoption guard: these fields can reinstall or otherwise mutate a live
  # traffic-bearing node. OpenTofu stores the imported server identity now; Talos
  # boot-chain convergence stays explicit until the full bootstrap graph owns it.
  allow_reinstall = false

  lifecycle {
    prevent_destroy = true
    ignore_changes = [
      billing,
      disk_layout,
      ipxe,
      operating_system,
      raid,
      ssh_keys,
      tags,
      user_data,
    ]
  }
}

resource "latitudesh_vlan_assignment" "control_plane" {
  for_each = local.control_plane_nodes

  server_id          = latitudesh_server.control_plane[each.key].id
  virtual_network_id = latitudesh_virtual_network.management.id

  lifecycle {
    prevent_destroy = true
  }
}

check "management_vlan_vid" {
  assert {
    condition     = latitudesh_virtual_network.management.vid == local.vlan.vid
    error_message = "Latitude VLAN VID drifted; expected VID 2140 for the guardian-mgmt L2 fabric."
  }
}

check "control_plane_public_ips" {
  assert {
    condition = alltrue([
      for name, node in local.control_plane_nodes :
      latitudesh_server.control_plane[name].primary_ipv4 == node.public_ipv4
    ])
    error_message = "Latitude server public IPv4s drifted from the checked-in guardian-mgmt inventory."
  }
}
