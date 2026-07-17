output "checkout_canary_product_id" {
  description = "Stripe product ID for the synthetic browser checkout canary."
  value       = stripe_product.checkout_canary.id
}

output "checkout_canary_price_id" {
  description = "Stripe price ID mounted into the payments service for the synthetic browser checkout canary."
  value       = stripe_price.checkout_canary_usd.id
}

output "payments_webhook_endpoint_id" {
  description = "Stripe webhook endpoint ID managed by this root."
  value       = stripe_webhook_endpoint.payments.id
}

output "payments_webhook_signing_secret" {
  description = "Creation-only signing secret relayed into OpenBao for the payments service."
  value       = stripe_webhook_endpoint.payments.secret
  sensitive   = true
}
