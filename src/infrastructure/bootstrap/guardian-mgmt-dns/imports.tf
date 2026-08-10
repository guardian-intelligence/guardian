# One-time adoption of resources that predate this root's in-cluster R2
# state: they were created by workstation applies whose state never
# migrated, so the first in-cluster apply raced live objects and failed on
# name/content uniqueness. Importing is the roll-forward fix — the config
# already describes these objects, so post-import the plan reduces to
# in-place drift correction. IDs are Cloudflare identifiers, not secrets.
#
# Deliberately NOT imported, because the first in-cluster apply already
# created state-tracked instances and Cloudflare allows duplicate names for
# them: the LB monitor, the three Access service tokens, and the three
# Access policies. The new instances are canonical; the pre-existing
# duplicates are deleted out-of-band after this converges. Consequence: the
# Access application flips to the new policies, so the Codex/Cursor/Devin
# environments must refresh their service-token secrets from this root's
# outputs (the standard rotation flow).
#
# Safe to delete this file once the root has converged Ready.

locals {
  import_account_id = "c3eaeffaadf7d4847684d4775c16d598"
  import_zone_gi    = "c952fb5989d232593ec9cca71030cb58" # guardianintelligence.org
  import_zone_rumi  = "034bf5d0a4ff33b0e9965f50be70d8d0" # rumi.engineering
  import_zone_wum   = "4bfa5c0e3d0183dc0e30b9c4cbc17d47" # wakeupmythra.com
}

import {
  to = cloudflare_load_balancer_pool.guardian_mgmt_ash
  id = "${local.import_account_id}/8a2f0a065687e2804c384576c70e8fd7"
}

import {
  to = cloudflare_load_balancer.guardian_mgmt_public["*.guardianintelligence.org"]
  id = "${local.import_zone_gi}/63b4a0aa39c7a1a0df05b6e194886c7d"
}

import {
  to = cloudflare_load_balancer.guardian_mgmt_public["guardianintelligence.org"]
  id = "${local.import_zone_gi}/663c507c74b28a19a88f74337988317b"
}

import {
  to = cloudflare_load_balancer.guardian_mgmt_public["api.guardianintelligence.org"]
  id = "${local.import_zone_gi}/71561654804b7e9a0de1ec3a616f9dbd"
}

import {
  to = cloudflare_load_balancer.guardian_mgmt_public["alerta.guardianintelligence.org"]
  id = "${local.import_zone_gi}/5a67c87dcfe6c9ea8d914e1aeccf39d3"
}

import {
  to = cloudflare_load_balancer.guardian_mgmt_public["dashboard.guardianintelligence.org"]
  id = "${local.import_zone_gi}/9eb93d560c54b86c1d7d84843157c8db"
}

import {
  to = cloudflare_load_balancer.guardian_mgmt_public["grafana.guardianintelligence.org"]
  id = "${local.import_zone_gi}/726c3d47984517e4906fbf29b59448c7"
}

import {
  to = cloudflare_load_balancer.guardian_mgmt_public["keycloak.guardianintelligence.org"]
  id = "${local.import_zone_gi}/e30967dadff9082961c43c138b711fa4"
}

import {
  to = cloudflare_dns_record.guardian_mgmt_k8s_api["ash-earth"]
  id = "${local.import_zone_gi}/e4ffbaff9bbd9c4e20c8cdb0d2a81f2f"
}

import {
  to = cloudflare_dns_record.guardian_mgmt_k8s_api["ash-wind"]
  id = "${local.import_zone_gi}/7e29b32f12adba1c4597e2e1f7dae5b7"
}

import {
  to = cloudflare_dns_record.guardian_mgmt_k8s_api["ash-water"]
  id = "${local.import_zone_gi}/fa847571908cd6b9b4fb538b8d81e274"
}

import {
  to = cloudflare_zero_trust_tunnel_cloudflared.guardian_codex_cloud
  id = "${local.import_account_id}/ace31a4d-3baf-4d5b-b9bd-41456511819f"
}

import {
  to = cloudflare_dns_record.guardian_codex_cloud_k8s_api
  id = "${local.import_zone_gi}/a2069e7c7e9911ef3c39902232138077"
}

import {
  to = cloudflare_zero_trust_access_application.guardian_codex_cloud
  id = "accounts/${local.import_account_id}/334a13d3-6711-43aa-b214-69960f7f1f75"
}

import {
  to = cloudflare_dns_record.rumi_engineering_apex["ash-earth"]
  id = "${local.import_zone_rumi}/7b8a1291f7014e041012f146465e46f6"
}

import {
  to = cloudflare_dns_record.rumi_engineering_apex["ash-wind"]
  id = "${local.import_zone_rumi}/f0249e4b71f0c7b769ebdc5cc562603f"
}

import {
  to = cloudflare_dns_record.rumi_engineering_apex["ash-water"]
  id = "${local.import_zone_rumi}/9819d80a0f993b195d0ad00684a73e93"
}

import {
  to = cloudflare_dns_record.rumi_engineering_caa["letsencrypt"]
  id = "${local.import_zone_rumi}/a526e3ebeb24a462452ccf03bd8245ad"
}

import {
  to = cloudflare_dns_record.rumi_engineering_caa["google"]
  id = "${local.import_zone_rumi}/96abc8e1a5ed9cdf55b39243fda726a0"
}

import {
  to = cloudflare_dns_record.wakeupmythra_com_apex["ash-earth"]
  id = "${local.import_zone_wum}/76e51fb68bf5403490206a6c8f5f09df"
}

import {
  to = cloudflare_dns_record.wakeupmythra_com_apex["ash-wind"]
  id = "${local.import_zone_wum}/411bfe32a407808a472ab87b6b8aff69"
}

import {
  to = cloudflare_dns_record.wakeupmythra_com_apex["ash-water"]
  id = "${local.import_zone_wum}/ff64089b6e22a16ca01991f3ca788f22"
}

import {
  to = cloudflare_dns_record.wakeupmythra_com_wt
  id = "${local.import_zone_wum}/3d090ccbdd40944aa6b997f41ca428c1"
}

import {
  to = cloudflare_dns_record.wakeupmythra_com_caa["letsencrypt"]
  id = "${local.import_zone_wum}/a155042d05be4625711d7c0eb488819f"
}

import {
  to = cloudflare_dns_record.wakeupmythra_com_caa["google"]
  id = "${local.import_zone_wum}/ff6398e98bdd7840b1ef7b23988d1468"
}
