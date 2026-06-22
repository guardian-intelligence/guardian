output "management_endpoints" {
  value = {
    api_vip             = local.vlan.api_vip
    api_endpoint        = local.api_endpoint
    talos_nodes         = local.node_private_ipv4
    public_ingress_ipv4 = local.node_public_ipv4
    metallb_pool        = local.vlan.metallb_pool
    pod_mtu             = local.vlan.pod_mtu
    vlan_mtu            = local.vlan.vlan_mtu
    vip_link            = local.vlan.vip_link
    environment_hosts   = { for name, env in local.environments : name => env.host }
    apps = {
      harbor     = "${local.harbor_app.metadata.namespace}/${local.harbor_app.metadata.name}"
      clickhouse = "${local.clickhouse_app.metadata.namespace}/${local.clickhouse_app.metadata.name}"
      postgres   = "${local.postgres_app.metadata.namespace}/${local.postgres_app.metadata.name}"
      openbao    = "${local.openbao_app.metadata.namespace}/${local.openbao_app.metadata.name}"
    }
  }
}
