# Lane-token minting for the guardianintelligence.org Cloudflare estate: every
# operational token is declared here and re-derivable from the custody minter,
# so custody carries one Cloudflare credential instead of one per lane.
# Rotation is explicit, never a side effect: bump local.expires (and taint the
# resource to re-mint), apply, then relay to the lane's consumer. The expiry
# check below turns every routine plan into the renewal reminder.

locals {
  zone_id            = "c952fb5989d232593ec9cca71030cb58" # guardianintelligence.org
  rumi_zone_id       = "RUMI_ZONE_ID_PENDING_ONBOARD"     # rumi.engineering — paste from the dashboard after the zone is added to the account
  account_resource   = "com.cloudflare.api.account.${var.cloudflare_account_id}"
  zone_resource      = "com.cloudflare.api.account.zone.${local.zone_id}"
  rumi_zone_resource = "com.cloudflare.api.account.zone.${local.rumi_zone_id}"

  # Stable identifiers from GET /accounts/<id>/tokens/permission_groups.
  permission_groups = {
    zone_read               = "c8fed203ed3043cba015a93ad1616f1f" # Zone Read (zone)
    dns_read                = "82e64a83756745bbbb1c9c2701bf816b" # DNS Read (zone)
    dns_write               = "4755a26eedb94da69e1066d98aa820be" # DNS Write (zone)
    load_balancers_read     = "e9a975f628014f1d85b723993116f7d5" # Load Balancers Read (zone)
    load_balancers_write    = "6d7f2f5f5b1d4a0e9081fdc98d432fd1" # Load Balancers Write (zone)
    lb_monitors_pools_read  = "9d24387c6e8544e2bc4024a03991339f" # Load Balancing: Monitors and Pools Read (account)
    lb_monitors_pools_write = "d2a1802cc9a34e30852f8b33869b2f3c" # Load Balancing: Monitors and Pools Write (account)
    zone_settings_write     = "3030687196b94b638145a3953da2b699" # Zone Settings Write (zone)
    zone_dns_settings_write = "c4df38be41c247b3b4b7702e76eadae0" # Zone DNS Settings Write (zone)
    cache_settings_write    = "9ff81cbbe65c400b97d92c3c1033cab6" # Cache Settings Write (zone)
    bot_management_write    = "3b94c49258ec4573b06d51d99b6416c0" # Bot Management Write (zone)
    ssl_certificates_write  = "c03055bc037c4ea9afb9a9f104b7b721" # SSL and Certificates Write (zone)
    firewall_services_write = "43137f8d07884d3198dc0ee77ca6e79b" # Firewall Services Write (zone)
    r2_bucket_item_read     = "6a018a9f2fc74eb6b293b0c548f38b39" # Workers R2 Storage Bucket Item Read (bucket)
    r2_bucket_item_write    = "2efd5506f9c8494dacb1fa10a3e7d5b6" # Workers R2 Storage Bucket Item Write (bucket)
  }

  backups_bucket_resource = "com.cloudflare.edge.r2.bucket.${var.cloudflare_account_id}_default_guardian-backups"
  vault_bucket_resource   = "com.cloudflare.edge.r2.bucket.${var.cloudflare_account_id}_default_guardian-vault"

  expires = {
    dns_lb_provisioner    = "2026-10-06T00:00:00Z"
    external_dns          = "2026-10-06T00:00:00Z"
    edge_policy_provision = "2026-10-06T00:00:00Z"
    payments_journal      = "2026-10-06T00:00:00Z"
    r2_backups            = "2026-10-06T00:00:00Z"
    r2_state              = "2026-10-06T00:00:00Z"
  }
}

# Apply-time credential for the guardian-mgmt-dns root:
#   CLOUDFLARE_API_TOKEN=$(tofu output -raw dns_lb_provisioner_token_value)
# Monitors and pools are account-level objects in Cloudflare's model, hence
# the two policy statements.
resource "cloudflare_account_token" "dns_lb_provisioner" {
  account_id = var.cloudflare_account_id
  name       = "guardian-dns-lb-provisioner"
  expires_on = local.expires.dns_lb_provisioner

  policies = [
    {
      effect = "allow"
      permission_groups = [
        { id = local.permission_groups.lb_monitors_pools_read },
        { id = local.permission_groups.lb_monitors_pools_write },
      ]
      resources = jsonencode({ (local.account_resource) = "*" })
    },
    {
      effect = "allow"
      permission_groups = [
        { id = local.permission_groups.zone_read },
        { id = local.permission_groups.dns_read },
        { id = local.permission_groups.dns_write },
        { id = local.permission_groups.load_balancers_read },
        { id = local.permission_groups.load_balancers_write },
      ]
      resources = jsonencode({ (local.zone_resource) = "*", (local.rumi_zone_resource) = "*" })
    },
  ]
}

# In-cluster credential for the external-dns controller. The value is relayed
# into OpenBao at kv/guardian/guardian-mgmt/external-dns/cloudflare (property
# CF_API_TOKEN) via the guardian-writer-external-dns scoped role; ESO
# materializes it and the controller reads it as a secretKeyRef env var, so a
# rotation is relay + force-sync + pod restart.
resource "cloudflare_account_token" "external_dns" {
  account_id = var.cloudflare_account_id
  name       = "guardian-external-dns"
  expires_on = local.expires.external_dns

  policies = [
    {
      effect = "allow"
      permission_groups = [
        { id = local.permission_groups.zone_read },
        { id = local.permission_groups.dns_read },
        { id = local.permission_groups.dns_write },
      ]
      resources = jsonencode({ (local.zone_resource) = "*" })
    },
  ]
}

# Apply-time credential for the guardian-mgmt-edge-policy root. Zone policy
# writes only — no DNS-record or LB permission, so it cannot move traffic.
resource "cloudflare_account_token" "edge_policy_provisioner" {
  account_id = var.cloudflare_account_id
  name       = "guardian-edge-policy-provisioner"
  expires_on = local.expires.edge_policy_provision

  policies = [
    {
      effect = "allow"
      permission_groups = [
        { id = local.permission_groups.zone_read },
        { id = local.permission_groups.zone_settings_write },
        { id = local.permission_groups.zone_dns_settings_write },
        { id = local.permission_groups.cache_settings_write },
        { id = local.permission_groups.bot_management_write },
        { id = local.permission_groups.ssl_certificates_write },
        { id = local.permission_groups.firewall_services_write },
      ]
      resources = jsonencode({ (local.zone_resource) = "*", (local.rumi_zone_resource) = "*" })
    },
  ]
}

# Backup-storage credential (guardian-backups bucket only). R2 tokens ARE
# account tokens; the S3 keypair is derived, not returned: access key = token
# id, secret key = SHA-256 hex of the token value (verified live with a probe
# token: derived pair authenticates, wrong secret fails signature validation).
# The value is relayed to kv/guardian/guardian-mgmt/tenant-root/backups-r2 as
# the flat accessKey/secretKey keys the backupstrategy-controller projector
# consumes; the ClickHouse backup sidecar loads the Secret at container start,
# so a rotation is relay + force-sync + CH pod recycle.
resource "cloudflare_account_token" "r2_backups" {
  account_id = var.cloudflare_account_id
  name       = "guardian-r2-backups"
  expires_on = local.expires.r2_backups

  policies = [
    {
      effect = "allow"
      permission_groups = [
        { id = local.permission_groups.r2_bucket_item_read },
        { id = local.permission_groups.r2_bucket_item_write },
      ]
      resources = jsonencode({ (local.backups_bucket_resource) = "*" })
    },
  ]
}

# Payment-ledger recovery journal credential. It can read and append objects
# only in guardian-backups; the payments workload never receives the backup
# controller's credential or access to the credential-bearing guardian-vault
# state bucket. Journal object keys are further isolated under
# tenant-guardian-prod/payments-journal.
resource "cloudflare_account_token" "payments_journal" {
  account_id = var.cloudflare_account_id
  name       = "guardian-payments-journal"
  expires_on = local.expires.payments_journal

  policies = [
    {
      effect = "allow"
      permission_groups = [
        { id = local.permission_groups.r2_bucket_item_read },
        { id = local.permission_groups.r2_bucket_item_write },
      ]
      resources = jsonencode({ (local.backups_bucket_resource) = "*" })
    },
  ]
}

# Tofu state-backend credential (guardian-vault bucket only — tighter than
# the account-wide R2 token it replaces). Self-reference caveat: THIS root's
# backend also uses it, so the pair must be custody-mirrored (env-set
# cloudflare_r2_access_key_id / cloudflare_r2_secret_access_key) after every
# rotation — at DR time the backend needs the keys before any state is
# readable. Rotate by minting the successor while applying with the
# predecessor; the swap is an env change, never a state edit.
resource "cloudflare_account_token" "r2_state" {
  account_id = var.cloudflare_account_id
  name       = "guardian-r2-tofu-state"
  expires_on = local.expires.r2_state

  policies = [
    {
      effect = "allow"
      permission_groups = [
        { id = local.permission_groups.r2_bucket_item_read },
        { id = local.permission_groups.r2_bucket_item_write },
      ]
      resources = jsonencode({ (local.vault_bucket_resource) = "*" })
    },
  ]
}

check "token_expiry_horizon" {
  assert {
    condition = alltrue([
      for name, expiry in local.expires :
      timecmp(timeadd(plantimestamp(), "504h"), expiry) < 0
    ])
    error_message = "A lane token expires within 21 days. Rotate it: bump local.expires, taint the token resource, apply, relay to the lane's consumer."
  }
}
