variable "cloudflare_account_id" {
  description = "Cloudflare account id for the guardianintelligence.org zone."
  type        = string
}

variable "cloudflare_lb_monitor_interval_seconds" {
  description = "Cloudflare Load Balancing health monitor interval. Pro plans require 60s minimum; Business allows 15s; Enterprise allows 10s."
  type        = number
  default     = 60

  validation {
    condition     = contains([10, 15, 60], var.cloudflare_lb_monitor_interval_seconds)
    error_message = "cloudflare_lb_monitor_interval_seconds must be 10, 15, or 60."
  }
}

variable "cloudflare_lb_check_regions" {
  description = "Cloudflare Load Balancing region codes used to probe the ASH origin pool."
  type        = list(string)
  default = [
    "ENAM",
  ]

  validation {
    condition     = length(var.cloudflare_lb_check_regions) > 0
    error_message = "cloudflare_lb_check_regions must contain at least one region."
  }
}
