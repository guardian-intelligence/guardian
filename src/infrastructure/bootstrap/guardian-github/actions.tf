# The workflow pins and the GitHub setting share one reviewed declaration.
# A new action needs this policy reconciled before a workflow can use it:
# GitHub rejects disallowed refs before starting any steps, including the gate.
locals {
  actions_allowlist = jsondecode(file("${path.module}/../../../../.github/actions-allowlist.json"))
}

import {
  to = github_actions_repository_permissions.guardian
  id = "guardian"
}

resource "github_actions_repository_permissions" "guardian" {
  repository           = "guardian"
  enabled              = true
  allowed_actions      = "selected"
  sha_pinning_required = true

  allowed_actions_config {
    github_owned_allowed = local.actions_allowlist.github_owned_allowed
    verified_allowed     = local.actions_allowlist.verified_allowed
    patterns_allowed     = local.actions_allowlist.patterns_allowed
  }

  # Provider deletion restores an allow-all policy; retiring management must
  # use a reviewed removed block with destroy=false instead.
  lifecycle {
    prevent_destroy = true
  }
}
