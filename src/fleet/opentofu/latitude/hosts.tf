locals {
  latitude_project_id = "proj_ZWr75Zdbm0A91"

  hosts = {
    ash-bm-001 = {
      hostname         = "gi-ash-bm-001"
      site             = "ASH"
      plan             = "f4-metal-small"
      billing          = "monthly"
      operating_system = "ubuntu_24_04_x64_lts"
      allow_reinstall  = true
      ssh_keys         = ["ssh_BDXM5Ermoarpk"]
      user_data        = "ud_jv6m5Jg2daLPe"
      tags = [
        "guardian",
        "asset:ash-bm-001",
        "cluster:guardian-nonprod",
        "environment:dev",
        "role:control-plane",
      ]
    }
  }
}

resource "latitudesh_server" "host" {
  for_each = local.hosts

  hostname         = each.value.hostname
  project          = local.latitude_project_id
  site             = each.value.site
  plan             = each.value.plan
  billing          = each.value.billing
  operating_system = each.value.operating_system
  allow_reinstall  = each.value.allow_reinstall
  ssh_keys         = each.value.ssh_keys
  user_data        = each.value.user_data
  tags             = each.value.tags

  lifecycle {
    prevent_destroy = true
  }
}
