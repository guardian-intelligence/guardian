output "hosts" {
  description = "Latitude host inventory tracked by this stack."
  value = {
    for asset, server in latitudesh_server.host : asset => {
      id           = server.id
      hostname     = server.hostname
      primary_ipv4 = server.primary_ipv4
      site         = server.site
      plan         = server.plan
      tags         = server.tags
    }
  }
}
