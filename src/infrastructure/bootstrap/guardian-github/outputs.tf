output "required_check" {
  description = "The status check context main's ruleset requires. Must equal the build-and-test job key."
  value       = local.required_check
}

output "customer_fleet_repositories" {
  description = "Simulated customer repositories under management, for the canary loop's inventory."
  value       = sort(keys(local.customer_fleet))
}
