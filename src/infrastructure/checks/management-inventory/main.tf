locals {
  inventory = jsondecode(file("${path.module}/../../inventory/guardian-mgmt.json"))
  vlan      = local.inventory.network.vlan

  environments = {
    for env in local.inventory.environments : env.name => env
  }
  environment_files = {
    dev   = yamldecode(file("${path.module}/../../../environments/dev/environment.yaml"))
    gamma = yamldecode(file("${path.module}/../../../environments/gamma/environment.yaml"))
    prod  = yamldecode(file("${path.module}/../../../environments/prod/environment.yaml"))
  }

  node_public_ipv4  = [for node in local.inventory.nodes : node.public_ipv4]
  node_private_ipv4 = [for node in local.inventory.nodes : node.private_ipv4]
  api_endpoint      = "https://${local.vlan.api_vip}:6443"

  base_kustomization = yamldecode(file("${path.module}/../../base/kustomization.yaml"))
  required_base_resources = [
    "apps/clickhouse.yaml",
    "apps/harbor.yaml",
    "apps/postgres.yaml",
    "backups/managed-databases.yaml",
    "cozystack/platform.yaml",
    "networking/metallb.yaml",
    "networking/subnet-mtu.yaml",
    "storage/storageclasses.yaml",
    "storage/linstor-satellite-config.yaml",
    "openbao/openbao.yaml",
    "openbao/networkpolicy.yaml",
    "products/company-site.yaml",
    "tenants/environments.yaml",
    "tenants/root.yaml",
  ]

  harbor_app     = yamldecode(file("${path.module}/../../base/apps/harbor.yaml"))
  clickhouse_app = yamldecode(file("${path.module}/../../base/apps/clickhouse.yaml"))
  postgres_app   = yamldecode(file("${path.module}/../../base/apps/postgres.yaml"))
  openbao_app    = yamldecode(file("${path.module}/../../base/openbao/openbao.yaml"))

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

  tenant_docs = concat(
    [
      for raw in split("\n---\n", file("${path.module}/../../base/tenants/environments.yaml")) :
      yamldecode(raw)
      if trimspace(raw) != "" && strcontains(raw, "apiVersion:")
    ],
    [yamldecode(file("${path.module}/../../base/tenants/root.yaml"))],
  )
  tenant_hosts = {
    for doc in local.tenant_docs : doc.metadata.name => doc.spec.host
  }

  company_site_docs = [
    for raw in split("\n---\n", file("${path.module}/../../base/products/company-site.yaml")) :
    yamldecode(raw)
    if trimspace(raw) != ""
  ]
  company_site_ingress_hosts = {
    for doc in local.company_site_docs : doc.metadata.namespace => [
      for rule in doc.spec.rules : rule.host
    ]
    if doc.kind == "Ingress" && doc.metadata.name == "company-site"
  }
  company_site_deployment_images = {
    for doc in local.company_site_docs : doc.metadata.namespace => one([
      for container in doc.spec.template.spec.containers : container.image
      if container.name == "company-site"
    ])
    if doc.kind == "Deployment" && doc.metadata.name == "company-site"
  }

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

check "base_kustomization_contains_required_resources" {
  assert {
    condition = length(setsubtract(
      toset(local.required_base_resources),
      toset(local.base_kustomization.resources),
    )) == 0
    error_message = "src/infrastructure/base/kustomization.yaml must include all required management-cluster component manifests."
  }
}

check "required_app_crs_match_management_surface" {
  assert {
    condition = (
      local.harbor_app.apiVersion == "apps.cozystack.io/v1alpha1" &&
      local.harbor_app.kind == "Harbor" &&
      local.harbor_app.metadata.name == "oci" &&
      local.harbor_app.metadata.namespace == "tenant-root" &&
      local.harbor_app.spec.host == "oci.guardianintelligence.org" &&
      local.clickhouse_app.kind == "ClickHouse" &&
      local.clickhouse_app.metadata.name == "ledger" &&
      local.clickhouse_app.metadata.namespace == "tenant-root" &&
      local.postgres_app.kind == "Postgres" &&
      local.postgres_app.metadata.name == "guardian" &&
      local.postgres_app.metadata.namespace == "tenant-root" &&
      local.openbao_app.kind == "OpenBAO" &&
      local.openbao_app.metadata.name == "guardian" &&
      local.openbao_app.metadata.namespace == "tenant-root"
    )
    error_message = "Required Harbor, ClickHouse, Postgres, and OpenBao app CRs must keep their expected names and tenant-root namespace."
  }
}

check "cozystack_api_endpoint_matches_inventory_vip" {
  assert {
    condition     = local.cozystack_values.publishing.apiServerEndpoint == local.api_endpoint
    error_message = "Cozystack platform apiServerEndpoint must match the inventory API VIP."
  }
}

check "cozystack_platform_exposes_required_services" {
  assert {
    condition = (
      local.cozystack_platform.kind == "Package" &&
      local.cozystack_platform.metadata.name == "cozystack.cozystack-platform" &&
      local.cozystack_platform.spec.variant == "isp-full" &&
      local.cozystack_values.publishing.host == local.environments.prod.host &&
      contains(local.cozystack_values.publishing.exposedServices, "dashboard") &&
      contains(local.cozystack_values.publishing.exposedServices, "api")
    )
    error_message = "Cozystack platform must use the isp-full variant and expose dashboard/api for the management cluster."
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

check "environment_files_match_inventory" {
  assert {
    condition = alltrue([
      for name, env_file in local.environment_files :
      env_file.cluster == local.inventory.cluster &&
      env_file.tenant.namespace == local.environments[name].tenant &&
      env_file.tenant.parent == local.environments[name].parent_tenant &&
      env_file.tenant.host == local.environments[name].host &&
      env_file.surfaces.companySite.host == local.environments[name].surfaces.company_site.host &&
      env_file.surfaces.companySite.routes == local.environments[name].surfaces.company_site.routes
    ])
    error_message = "Environment YAML files must match src/infrastructure/inventory/guardian-mgmt.json."
  }
}

check "tenant_hosts_match_inventory" {
  assert {
    condition = (
      local.tenant_hosts.dev == local.environments.dev.host &&
      local.tenant_hosts.gamma == local.environments.gamma.host &&
      local.tenant_hosts.root == local.environments.prod.host
    )
    error_message = "Cozystack Tenant hosts must match the environment hosts in inventory."
  }
}

check "company_site_ingress_hosts_match_inventory" {
  assert {
    condition = alltrue([
      for name, env in local.environments :
      local.company_site_ingress_hosts[env.tenant] == [env.host]
    ])
    error_message = "Company-site Ingress hosts must match inventory environment hosts."
  }
}

check "company_site_images_match_environment_files" {
  assert {
    condition = alltrue([
      for name, env in local.environments :
      local.company_site_deployment_images[env.tenant] == local.environment_files[name].surfaces.companySite.image
    ])
    error_message = "Company-site Deployment images must match the environment YAML image digests."
  }
}

check "dns_records_cover_environment_hosts" {
  assert {
    condition = length(setsubtract(
      toset([for _, env in local.environments : env.host]),
      toset([
        for record in local.inventory.publishing.dns_records : record.name
        if record.type == "A" && record.targets == "management_public_ipv4"
      ]),
    )) == 0
    error_message = "Cloudflare DNS inventory must include A records for every environment host."
  }
}
