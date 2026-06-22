data "latitudesh_region" "ash" {
  slug = local.site
}

data "latitudesh_plan" "f4_metal_small" {
  name = "f4.metal.small"
}

locals {
  project_id = "proj_R82A0yqmd06mM"
  site       = "ASH"
  vlan = {
    id           = "vlan_8mop5gkpP5jxv"
    vid          = 2140
    description  = "guardian-mgmt L2 fabric"
    subnet       = "10.8.0.0/24"
    vlan_mtu     = 1420
    pod_mtu      = 1362
    api_vip      = "10.8.0.250"
    vip_link     = "enp1s0f0.2140"
    metallb_pool = "10.8.0.200-10.8.0.240"
  }
  control_plane_nodes = {
    ash-earth = {
      name         = "ash-earth"
      server_id    = "sv_vAPXaMxKM5epz"
      hostname     = "ash-earth"
      public_ipv4  = "206.223.228.101"
      private_ipv4 = "10.8.0.11"
    }
    ash-wind = {
      name         = "ash-wind"
      server_id    = "sv_nPRbajqEB5koM"
      hostname     = "ash-wind"
      public_ipv4  = "45.250.254.119"
      private_ipv4 = "10.8.0.12"
    }
    ash-water = {
      name         = "ash-water"
      server_id    = "sv_8mop5gZo8Njxv"
      hostname     = "ash-water"
      public_ipv4  = "206.223.228.87"
      private_ipv4 = "10.8.0.13"
    }
  }
}

resource "latitudesh_virtual_network" "management" {
  description = local.vlan.description
  project     = local.project_id
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
  project          = local.project_id
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
    error_message = "Latitude server public IPv4s drifted from the checked-in guardian-mgmt OpenTofu topology."
  }
}
