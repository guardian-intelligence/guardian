locals {
  cloudflare_zone_name  = "guardianintelligence.org"
  external_dns_owner_id = "guardian-mgmt-ash"
  public_ingress_origin_names = [
    "ash-earth",
    "ash-wind",
    "ash-water",
  ]
  public_ingress_origins = {
    ash-earth = {
      public_ipv4 = "206.223.228.101"
    }
    ash-wind = {
      public_ipv4 = "45.250.254.119"
    }
    ash-water = {
      public_ipv4 = "206.223.228.87"
    }
  }
  public_edge_hostnames = [
    "*.guardianintelligence.org",
    "guardianintelligence.org",
    "api.guardianintelligence.org",
    "alerta.guardianintelligence.org",
    "dashboard.guardianintelligence.org",
    "grafana.guardianintelligence.org",
    "keycloak.guardianintelligence.org",
  ]

  public_ingress_ipv4s = [
    for name in local.public_ingress_origin_names :
    local.public_ingress_origins[name].public_ipv4
  ]

  # The node whose public IP terminates the WUM game plane (UDP 4433).
  # ash-worker0 hosts chunkies-gateway; the IP matches its Talos declaration
  # in guardian-mgmt/main.tf.
  game_plane_origin_ipv4 = "206.223.228.99"

  k8s_api_hostname             = "k8s.${local.cloudflare_zone_name}"
  codex_cloud_k8s_api_hostname = "k8s-codex.${local.cloudflare_zone_name}"
  cloud_agent_providers        = toset(["cursor", "devin"])

  # The operator route: the founder's workstation reaches the Kubernetes and
  # Talos APIs through the same Tunnel, each behind its own identity-gated
  # Access application, so no host outside the cluster needs allowlisting.
  operator_emails = ["im.shovonhasan@gmail.com"]
  operator_tunnel_routes = {
    kubernetes = {
      hostname = "k8s-operator.${local.cloudflare_zone_name}"
      service  = "tcp://kubernetes.default.svc:443"
      title    = "Kubernetes"
    }
    talos = {
      hostname = "talos-operator.${local.cloudflare_zone_name}"
      service  = "tcp://talos.default.svc:50000"
      title    = "Talos"
    }
  }
}

data "cloudflare_zone" "guardianintelligence_org" {
  filter = {
    name = local.cloudflare_zone_name
    account = {
      id = var.cloudflare_account_id
    }
  }
}

resource "cloudflare_load_balancer_monitor" "guardian_mgmt_ingress" {
  account_id       = var.cloudflare_account_id
  description      = "guardian-mgmt ASH root ingress HTTPS health"
  type             = "https"
  method           = "GET"
  path             = "/"
  expected_codes   = "200,302"
  follow_redirects = false
  interval         = var.cloudflare_lb_monitor_interval_seconds
  retries          = 1
  timeout          = 5
  consecutive_down = 1
  consecutive_up   = 1
  probe_zone       = local.cloudflare_zone_name

  header = {
    Host = [
      "dashboard.guardianintelligence.org",
    ]
  }
}

resource "cloudflare_load_balancer_pool" "guardian_mgmt_ash" {
  account_id      = var.cloudflare_account_id
  name            = "guardian-mgmt-ash"
  description     = "Guardian management cluster ASH public ingress origins"
  enabled         = true
  minimum_origins = 1
  monitor         = cloudflare_load_balancer_monitor.guardian_mgmt_ingress.id
  check_regions   = var.cloudflare_lb_check_regions

  # The API returns pool origins sorted by name and origins is an ordered
  # list, so the config must emit the same order or every plan is a reorder.
  origins = [
    for name in sort(local.public_ingress_origin_names) : {
      name    = name
      address = local.public_ingress_origins[name].public_ipv4
      enabled = true
      weight  = 1
    }
  ]

  origin_steering = {
    policy = "random"
  }

  # With auto-apply there is no human between merge and execution:
  # prevent_destroy turns a would-be destroy of the ops-access path into a
  # failed apply and an alert, and the structural invariants that used to be
  # warn-only check blocks gate the plan itself here (checks block nothing
  # in OpenTofu).
  lifecycle {
    prevent_destroy = true

    precondition {
      condition     = length(local.public_edge_hostnames) == 7
      error_message = "Root public edge hostnames belong to Cloudflare Load Balancing."
    }

    precondition {
      condition     = length(local.public_ingress_ipv4s) == 3
      error_message = "Cloudflare Load Balancing must publish all three guardian-mgmt ASH control-plane origins."
    }
  }
}

resource "cloudflare_load_balancer" "guardian_mgmt_public" {
  for_each = toset(local.public_edge_hostnames)

  zone_id       = data.cloudflare_zone.guardianintelligence_org.id
  name          = each.value
  description   = "guardian-mgmt ASH public edge"
  enabled       = true
  fallback_pool = cloudflare_load_balancer_pool.guardian_mgmt_ash.id
  default_pools = [
    cloudflare_load_balancer_pool.guardian_mgmt_ash.id,
  ]
  proxied         = true
  steering_policy = "off"

  adaptive_routing = {
    failover_across_pools = false
  }

  lifecycle {
    prevent_destroy = true
  }
}

# Must resolve while the cluster is down, so not in-cluster ExternalDNS;
# Cloudflare cannot proxy the Kubernetes/Talos API ports, so proxied=false.
resource "cloudflare_dns_record" "guardian_mgmt_k8s_api" {
  for_each = local.public_ingress_origins

  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  name    = local.k8s_api_hostname
  type    = "A"
  content = each.value.public_ipv4
  ttl     = 300
  proxied = false
  comment = "guardian-mgmt ${each.key} control-plane API"

  lifecycle {
    prevent_destroy = true
  }
}

# Codex cloud reaches the private Kubernetes Service through a remotely
# managed Cloudflare Tunnel. The public hostname accepts no origin traffic:
# Access requires the one service token below, cloudflared carries the TCP
# stream over Cloudflare's edge, and Kubernetes OIDC/RBAC remains the final
# authority boundary.
resource "cloudflare_zero_trust_tunnel_cloudflared" "guardian_codex_cloud" {
  account_id = var.cloudflare_account_id
  name       = "guardian-codex-cloud-kubernetes"
  config_src = "cloudflare"
}

resource "cloudflare_zero_trust_tunnel_cloudflared_config" "guardian_codex_cloud" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.guardian_codex_cloud.id
  config = {
    ingress = [
      {
        hostname = local.codex_cloud_k8s_api_hostname
        service  = "tcp://kubernetes.default.svc:443"
      },
      {
        hostname = local.operator_tunnel_routes.kubernetes.hostname
        service  = local.operator_tunnel_routes.kubernetes.service
      },
      {
        hostname = local.operator_tunnel_routes.talos.hostname
        service  = local.operator_tunnel_routes.talos.service
      },
      {
        service = "http_status:404"
      },
    ]
  }
}

data "cloudflare_zero_trust_tunnel_cloudflared_token" "guardian_codex_cloud" {
  account_id = var.cloudflare_account_id
  tunnel_id  = cloudflare_zero_trust_tunnel_cloudflared.guardian_codex_cloud.id
}

resource "cloudflare_dns_record" "guardian_codex_cloud_k8s_api" {
  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  name    = local.codex_cloud_k8s_api_hostname
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.guardian_codex_cloud.id}.cfargotunnel.com"
  ttl     = 1
  proxied = true
  comment = "Guardian Kubernetes API through the Codex cloud Tunnel"

  lifecycle {
    prevent_destroy = true
  }
}

resource "cloudflare_zero_trust_access_service_token" "guardian_codex_cloud" {
  account_id = var.cloudflare_account_id
  name       = "guardian-codex-cloud-kubernetes"
  duration   = "2160h"

  # The expiry horizon blocks the plan instead of warning (checks block
  # nothing in OpenTofu): under auto-apply, a reconcile that starts failing
  # 21 days out is the rotation reminder, surfaced through the CR going
  # red and the alert that follows.
  lifecycle {
    create_before_destroy = true

    postcondition {
      condition     = timecmp(timeadd(plantimestamp(), "504h"), self.expires_at) < 0
      error_message = "The Codex cloud Access service token expires within 21 days. Rotate it by incrementing client_secret_version, update the Codex environment secrets, and prove the cloud canary."
    }
  }
}

# Cursor and Devin share the proven tunnel hostname but not credentials. The
# Access token authenticates transport only; Kubernetes still requires each
# provider's independently minted, short-lived delivery-read token.
resource "cloudflare_zero_trust_access_service_token" "guardian_cloud_agent" {
  for_each = local.cloud_agent_providers

  account_id = var.cloudflare_account_id
  name       = "guardian-${each.key}-cloud-kubernetes"
  duration   = "2160h"

  lifecycle {
    create_before_destroy = true

    postcondition {
      condition     = timecmp(timeadd(plantimestamp(), "504h"), self.expires_at) < 0
      error_message = "A cloud-agent Access service token expires within 21 days. Rotate the affected provider token, update only that provider environment, and prove its cloud canary."
    }
  }
}

resource "cloudflare_zero_trust_access_policy" "guardian_codex_cloud" {
  account_id = var.cloudflare_account_id
  name       = "Guardian Codex cloud Kubernetes service token"
  decision   = "non_identity"
  include = [
    {
      service_token = {
        token_id = cloudflare_zero_trust_access_service_token.guardian_codex_cloud.id
      }
    },
  ]
}

resource "cloudflare_zero_trust_access_policy" "guardian_cloud_agent" {
  for_each = local.cloud_agent_providers

  account_id = var.cloudflare_account_id
  name       = "Guardian ${title(each.key)} cloud Kubernetes service token"
  decision   = "non_identity"
  include = [
    {
      service_token = {
        token_id = cloudflare_zero_trust_access_service_token.guardian_cloud_agent[each.key].id
      }
    },
  ]
}

resource "cloudflare_zero_trust_access_application" "guardian_codex_cloud" {
  account_id = var.cloudflare_account_id
  type       = "self_hosted"
  name       = "Guardian Codex cloud Kubernetes"
  domain     = local.codex_cloud_k8s_api_hostname
  policies = [
    {
      id         = cloudflare_zero_trust_access_policy.guardian_codex_cloud.id
      precedence = 1
    },
    {
      id         = cloudflare_zero_trust_access_policy.guardian_cloud_agent["cursor"].id
      precedence = 2
    },
    {
      id         = cloudflare_zero_trust_access_policy.guardian_cloud_agent["devin"].id
      precedence = 3
    },
  ]
}

# The operator route. Access admits only the founder's identity (Cloudflare
# one-time PIN to the listed address); behind it the Kubernetes API still
# demands a Keycloak persona and the Talos API its mTLS client certificate.
# The workstation side is tools/ops/mgmt-tunnel.
resource "cloudflare_dns_record" "guardian_operator_tunnel" {
  for_each = local.operator_tunnel_routes

  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  name    = each.value.hostname
  type    = "CNAME"
  content = "${cloudflare_zero_trust_tunnel_cloudflared.guardian_codex_cloud.id}.cfargotunnel.com"
  ttl     = 1
  proxied = true
  comment = "Guardian ${each.value.title} API for the operator through the cluster Tunnel"

  lifecycle {
    prevent_destroy = true
  }
}

# How the founder proves the operator policy's address: Cloudflare mails a
# one-time PIN to it. The account's other login, Cloudflare account sign-in,
# admits account members only, and the founder address is not one.
resource "cloudflare_zero_trust_access_identity_provider" "one_time_pin" {
  account_id = var.cloudflare_account_id
  name       = "One-time PIN"
  type       = "onetimepin"
  config     = {}
}

resource "cloudflare_zero_trust_access_policy" "guardian_operator" {
  account_id = var.cloudflare_account_id
  name       = "Guardian operator identity"
  decision   = "allow"
  include = [
    for email in local.operator_emails : {
      email = {
        email = email
      }
    }
  ]
}

resource "cloudflare_zero_trust_access_application" "guardian_operator" {
  for_each = local.operator_tunnel_routes

  account_id = var.cloudflare_account_id
  type       = "self_hosted"
  name       = "Guardian operator ${each.value.title}"
  domain     = each.value.hostname
  policies = [
    {
      id         = cloudflare_zero_trust_access_policy.guardian_operator.id
      precedence = 1
    },
  ]
}

# Inbound mail for guardianintelligence.org: one public address,
# contact@, forwarded by Cloudflare Email Routing to the founder inbox. The
# zone's MX and SPF records are the ones Email Routing manages itself.
# Cloudflare forwards only to verified destinations: creating the address
# mails a verification link to the inbox, and the rule stays inert until it
# is clicked. Everything else sent to the zone is dropped, so a typo or a
# harvested role address never reaches the inbox.
resource "cloudflare_email_routing_address" "founder_inbox" {
  account_id = var.cloudflare_account_id
  email      = "integrations.anveio@gmail.com"
}

resource "cloudflare_email_routing_rule" "contact" {
  zone_id  = data.cloudflare_zone.guardianintelligence_org.id
  name     = "contact@ to the founder inbox"
  enabled  = true
  priority = 0

  matchers = [
    {
      type  = "literal"
      field = "to"
      value = "contact@${local.cloudflare_zone_name}"
    },
  ]

  actions = [
    {
      type  = "forward"
      value = [cloudflare_email_routing_address.founder_inbox.email]
    },
  ]
}

resource "cloudflare_email_routing_catch_all" "guardianintelligence_org" {
  zone_id = data.cloudflare_zone.guardianintelligence_org.id
  name    = "drop everything but contact@"
  enabled = true

  matchers = [
    {
      type = "all"
    },
  ]

  actions = [
    {
      type = "drop"
    },
  ]
}

# rumi.engineering — the PrivateCut product edge. Proxied A records straight to
# the three ASH origins: no Cloudflare Load Balancer for this zone, so there
# is no per-request origin health steering — an origin outage surfaces as
# 1/3 of requests erroring until the record set is amended. Accepted for a
# free product surface; upgrade to a Load Balancer when the traffic warrants
# the per-zone spend.
data "cloudflare_zone" "rumi_engineering" {
  filter = {
    name = "rumi.engineering"
    account = {
      id = var.cloudflare_account_id
    }
  }
}

resource "cloudflare_dns_record" "rumi_engineering_apex" {
  for_each = local.public_ingress_origins

  zone_id = data.cloudflare_zone.rumi_engineering.id
  name    = "rumi.engineering"
  type    = "A"
  content = each.value.public_ipv4
  ttl     = 1
  proxied = true
  comment = "privatecut ${each.key} product edge"
}

# CAA policy for the zone, owned here rather than inherited from its
# pre-Guardian life: Cloudflare edge certificates issue via Google Trust
# Services or Let's Encrypt; nothing else may issue for this zone.
resource "cloudflare_dns_record" "rumi_engineering_caa" {
  for_each = {
    letsencrypt = "letsencrypt.org"
    google      = "pki.goog"
  }

  zone_id = data.cloudflare_zone.rumi_engineering.id
  name    = "rumi.engineering"
  type    = "CAA"
  ttl     = 1
  data = {
    flags = 0
    tag   = "issue"
    value = each.value
  }
  comment = "privatecut edge certificate issuance policy"

  lifecycle {
    prevent_destroy = true
  }
}

data "cloudflare_zone" "wakeupmythra_com" {
  filter = {
    name = "wakeupmythra.com"
    account = {
      id = var.cloudflare_account_id
    }
  }
}

resource "cloudflare_dns_record" "wakeupmythra_com_apex" {
  for_each = local.public_ingress_origins

  zone_id = data.cloudflare_zone.wakeupmythra_com.id
  name    = "wakeupmythra.com"
  type    = "A"
  content = each.value.public_ipv4
  ttl     = 1
  proxied = true
  comment = "wake up mythra ${each.key} product edge"
}

resource "cloudflare_dns_record" "wakeupmythra_com_auth" {
  for_each = local.public_ingress_origins

  zone_id = data.cloudflare_zone.wakeupmythra_com.id
  name    = "auth.wakeupmythra.com"
  type    = "A"
  content = each.value.public_ipv4
  ttl     = 1
  proxied = true
  comment = "wake up mythra ${each.key} identity edge (Keycloak realm frontendUrl)"
}

# The game plane: browsers dial WebTransport/QUIC on this name directly -
# Cloudflare cannot proxy it - so the record is DNS-only and points at the
# node that binds UDP 4433 (the chunkies-gateway hostNetwork pin on
# ash-worker0). Multi-node game fleets later mean more records here, not a
# proxy.
resource "cloudflare_dns_record" "wakeupmythra_com_wt" {
  zone_id = data.cloudflare_zone.wakeupmythra_com.id
  name    = "wt.wakeupmythra.com"
  type    = "A"
  content = local.game_plane_origin_ipv4
  ttl     = 300
  proxied = false
  comment = "wake up mythra game plane (WebTransport, dialed direct)"
}

# CAA: the Cloudflare edge issues via Google Trust Services or Let's
# Encrypt, and the wt game-plane certificate issues via Let's Encrypt
# (cert-manager DNS-01). Nothing else may issue for this zone.
resource "cloudflare_dns_record" "wakeupmythra_com_caa" {
  for_each = {
    letsencrypt = "letsencrypt.org"
    google      = "pki.goog"
  }

  zone_id = data.cloudflare_zone.wakeupmythra_com.id
  name    = "wakeupmythra.com"
  type    = "CAA"
  ttl     = 1
  data = {
    flags = 0
    tag   = "issue"
    value = each.value
  }
  comment = "wake up mythra certificate issuance policy"

  lifecycle {
    prevent_destroy = true
  }
}

# anveio.com — Shovon's contact card (src/anveio/cal). Proxied A records to
# the three ASH origins for the apex and www (the origin redirects www to the
# apex), with the same no-Load-Balancer trade-off as rumi.engineering. The
# zone's pre-Guardian records were retired; only its Resend/SES mail-sending
# records (send.anveio.com, notify.anveio.com, DKIM, DMARC) remain outside
# this root.
data "cloudflare_zone" "anveio_com" {
  filter = {
    name = "anveio.com"
    account = {
      id = var.cloudflare_account_id
    }
  }
}

resource "cloudflare_dns_record" "anveio_com_web" {
  for_each = {
    for pair in setproduct(["anveio.com", "www.anveio.com"], keys(local.public_ingress_origins)) :
    "${pair[0]}/${pair[1]}" => { name = pair[0], origin = pair[1] }
  }

  zone_id = data.cloudflare_zone.anveio_com.id
  name    = each.value.name
  type    = "A"
  content = local.public_ingress_origins[each.value.origin].public_ipv4
  ttl     = 1
  proxied = true
  comment = "anveio ${each.value.origin} product edge"
}

# CAA policy, as for rumi.engineering: Cloudflare edge certificates issue via
# Google Trust Services or Let's Encrypt; nothing else may issue for the zone.
resource "cloudflare_dns_record" "anveio_com_caa" {
  for_each = {
    letsencrypt = "letsencrypt.org"
    google      = "pki.goog"
  }

  zone_id = data.cloudflare_zone.anveio_com.id
  name    = "anveio.com"
  type    = "CAA"
  ttl     = 1
  data = {
    flags = 0
    tag   = "issue"
    value = each.value
  }
  comment = "anveio edge certificate issuance policy"

  lifecycle {
    prevent_destroy = true
  }
}

# Inbound mail for anveio.com: careers@, the address on the job descriptions,
# forwarded to the founder inbox; everything else is dropped, as for
# guardianintelligence.org. Email Routing itself is enabled on the zone by the
# edge-policy root (it needs Zone Settings Write); it adds and locks the MX
# records. The apex SPF record, adopted from the pre-Guardian zone, is owned
# here and carries the Email Routing include so forwarded mail passes SPF on
# its rewritten envelope. Resend sends
# from send.anveio.com, which has its own SPF, so the apex carries no SES
# include.
resource "cloudflare_email_routing_rule" "anveio_careers" {
  zone_id  = data.cloudflare_zone.anveio_com.id
  name     = "careers@ to the founder inbox"
  enabled  = true
  priority = 0

  matchers = [
    {
      type  = "literal"
      field = "to"
      value = "careers@anveio.com"
    },
  ]

  actions = [
    {
      type  = "forward"
      value = [cloudflare_email_routing_address.founder_inbox.email]
    },
  ]
}

resource "cloudflare_email_routing_catch_all" "anveio_com" {
  zone_id = data.cloudflare_zone.anveio_com.id
  name    = "drop everything but careers@"
  enabled = true

  matchers = [
    {
      type = "all"
    },
  ]

  actions = [
    {
      type = "drop"
    },
  ]
}

resource "cloudflare_dns_record" "anveio_com_spf" {
  zone_id = data.cloudflare_zone.anveio_com.id
  name    = "anveio.com"
  type    = "TXT"
  content = "\"v=spf1 include:_spf.mx.cloudflare.net ~all\""
  ttl     = 1
  comment = "anveio Email Routing SPF"
}
