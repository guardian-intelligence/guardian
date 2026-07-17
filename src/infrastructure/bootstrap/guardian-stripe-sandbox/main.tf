# Stripe is a payment rail, not Guardian's product catalog or balance
# authority. These objects describe only the synthetic checkout surface used
# to continuously prove the sandbox rail. PostgreSQL owns commercial policy
# and TigerBeetle ledger 2 owns the synthetic balance-changing facts.
resource "stripe_product" "checkout_canary" {
  name        = "Guardian synthetic checkout canary"
  description = "Automated sandbox-only end-to-end checkout probe"

  metadata = {
    guardian_environment = "sandbox"
    guardian_ledger_id   = "2"
    guardian_managed_by  = "opentofu"
    guardian_synthetic   = "true"
  }
}

resource "stripe_price" "checkout_canary_usd" {
  product     = stripe_product.checkout_canary.id
  currency    = "usd"
  unit_amount = 50
  lookup_key  = "guardian_synthetic_checkout_canary_usd_v1"
  nickname    = "Guardian synthetic checkout canary USD v1"

  metadata = {
    guardian_environment = "sandbox"
    guardian_ledger_id   = "2"
    guardian_managed_by  = "opentofu"
    guardian_synthetic   = "true"
  }
}

resource "stripe_webhook_endpoint" "payments" {
  url = "https://guardianintelligence.org/api/payments/v1/stripe/webhook"
  # The provider's v1 endpoint schema and stripe-go/v83 share this exact
  # contract. Do not advance one side without advancing the other.
  api_version = "2025-10-29.clover"

  # Subscribe only to events the projector makes authoritative. Expanding
  # this set requires implementing and testing the corresponding immutable
  # ledger movement first.
  enabled_events = [
    "payment_intent.succeeded",
  ]

  description = "Guardian sandbox payment rail to TigerBeetle ledger 2"
  metadata = {
    guardian_environment = "sandbox"
    guardian_ledger_id   = "2"
    guardian_managed_by  = "opentofu"
  }
}
