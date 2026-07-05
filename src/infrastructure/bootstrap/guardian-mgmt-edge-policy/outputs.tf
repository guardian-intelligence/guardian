output "authenticated_origin_pulls_enabled" {
  description = "Whether zone-level Authenticated Origin Pulls is enabled; origin-side enforcement is configured in ingress-nginx."
  value       = cloudflare_authenticated_origin_pulls.guardianintelligence_org.enabled
}

output "bot_fight_mode" {
  description = "Bot Fight Mode disposition; kept off so first-party event beacons are never challenged."
  value       = cloudflare_bot_management.guardianintelligence_org.fight_mode
}
