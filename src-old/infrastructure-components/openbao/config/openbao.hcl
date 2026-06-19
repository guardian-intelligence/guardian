# OpenBao removed Vault's mlock support entirely; there is no disable_mlock
# field and no IPC_LOCK capability requirement.
ui = false

storage "raft" {
  path = "/openbao/data"
  # node_id is injected per node via BAO_RAFT_NODE_ID; api_addr and
  # cluster_addr via BAO_API_ADDR / BAO_CLUSTER_ADDR.
}

listener "tcp" {
  address         = "0.0.0.0:8200"
  cluster_address = "0.0.0.0:8201"

  # Dev playground only. Serving-cert issuance is part of the cluster
  # tracer; this listener must grow TLS before any non-dev use.
  tls_disable = true
}

telemetry {
  disable_hostname = true
}
