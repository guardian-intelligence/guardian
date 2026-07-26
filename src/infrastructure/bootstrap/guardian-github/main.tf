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

  # The same App's installation on guardian-intelligence, which enumerates the
  # selected repositories its tokens can be minted for.
  promotions_installation_id = 144138265

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

# The Homebrew tap. brew's naming convention is what makes
# `brew install guardian-intelligence/tap/postflight` resolve, and the
# repository must be public for an anonymous `brew tap` to clone it. The
# postflight-cli release cutter's stable path renders Formula/postflight.rb
# and PUTs it here with a tap-scoped guardian-promotions token; nothing else
# writes to it, and hand edits are overwritten on the next stable cut.
resource "github_repository" "homebrew_tap" {
  name        = "homebrew-tap"
  description = "Homebrew tap for Guardian Intelligence tools"
  visibility  = "public"

  has_issues   = false
  has_projects = false
  has_wiki     = false

  # A default branch from the first apply, so the tap is clonable before the
  # first stable cut lands a formula on it.
  auto_init = true

  lifecycle {
    prevent_destroy = true
  }
}

# guardian-promotions is installed on selected repositories only, and the
# cutter mints its tap token by repository name — which fails the release
# before anything is published unless the installation covers the tap. The
# grant is part of the tap's definition, not a ceremony to remember.
resource "github_app_installation_repository" "promotions_homebrew_tap" {
  installation_id = local.promotions_installation_id
  repository      = github_repository.homebrew_tap.name
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
