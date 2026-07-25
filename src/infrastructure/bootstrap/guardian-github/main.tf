# GitHub is a control plane like any other, and until this root existed it was
# the only one edited by hand. On 2026-07-24 a PR renamed every CI check while
# main's ruleset kept requiring the four old contexts; those workflows no
# longer existed, so the contexts could never report and every branch cut after
# the rename sat BLOCKED on checks that would never run. Nothing caught it
# because the failure mode is a *pending* required check, not a red one.
#
# Two organizations are described here. guardian-intelligence holds the
# product; digital-guardian-software holds the simulated customer fleet, which
# exercises Postflight over real pull requests in repositories a real customer
# would recognise. Only the fleet repositories are managed — that org predates
# Guardian and its other repositories are none of this root's business.

locals {
  # The context GitHub reports for the repository's only CI gate. For a
  # single-job workflow that is the job key in build-and-test.yml, NOT the
  # workflow name. Renaming that job without changing this literal merge-locks
  # the repository; //src/infrastructure/tests asserts the two stay equal so
  # the rename is caught in CI instead of on the next PR that cannot merge.
  required_check = "build-and-test"

  # guardian-promotions[bot], the App that pushes rendered image pins straight
  # to main. It bypasses the ruleset by design — see the imageops runbook.
  promotions_app_id = 4206397

  # The simulated customer fleet. A new language or framework is a new entry
  # here and nothing else: the ecosystem drives what Postflight has to cache,
  # restore, and bill, and each repository carries its own upstream patch that
  # the canary loop cycles through a real pull request.
  #
  # Repositories are never destroyed by this root (see prevent_destroy below).
  # They accumulate the pull-request history the billing showback is
  # reconciled against; losing one loses the evidence, not just the fixture.
  customer_fleet = {
    "postflight-canary" = {
      description = "Turborepo build, cycling vercel/turborepo#13426 through Postflight"
    }
    "simulated-customer-node" = {
      description = "Node/pnpm customer workload"
    }
    "simulated-customer-go" = {
      description = "Go module customer workload"
    }
    "simulated-customer-python" = {
      description = "Python/uv customer workload"
    }
    "simulated-customer-gradle" = {
      description = "Gradle/JVM customer workload"
    }
  }
}

resource "github_repository_ruleset" "guardian_main" {
  name        = "main-protection"
  repository  = "guardian"
  target      = "branch"
  enforcement = "active"

  conditions {
    ref_name {
      include = ["refs/heads/main"]
      exclude = []
    }
  }

  bypass_actors {
    actor_id    = local.promotions_app_id
    actor_type  = "Integration"
    bypass_mode = "always"
  }

  rules {
    deletion         = true
    non_fast_forward = true

    required_status_checks {
      strict_required_status_checks_policy = false
      do_not_enforce_on_create             = false

      required_check {
        context = local.required_check
      }
    }
  }
}

# The fleet's pull requests are opened and merged by the canary loop, not by
# people, so these repositories carry no ruleset: a required check would stall
# the loop against a gate nothing reports. Their protection is that they are
# private, single-purpose, and not destroyable from here.
resource "github_repository" "customer_fleet" {
  provider = github.customer
  for_each = local.customer_fleet

  name        = each.key
  description = each.value.description
  visibility  = "private"

  has_issues   = false
  has_projects = false
  has_wiki     = false

  # The canary loop merges with a squash and expects the head branch to go
  # away; a fleet that accumulates merged branches drifts from what a customer
  # repository looks like after a few thousand builds.
  allow_squash_merge     = true
  allow_merge_commit     = false
  allow_rebase_merge     = false
  delete_branch_on_merge = true

  # Import-only: every repository here already exists and holds pull-request
  # history. This root describes them so a sixth is one map entry, never so
  # that a bad plan can remove one.
  lifecycle {
    prevent_destroy = true
  }
}
