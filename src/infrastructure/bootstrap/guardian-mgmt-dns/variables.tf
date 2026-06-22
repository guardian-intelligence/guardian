variable "aws_region" {
  description = "AWS region used by the Route53 provider."
  type        = string
  default     = "us-east-1"
}

variable "cloudflare_account_id" {
  description = "Cloudflare account id for the guardianintelligence.org zone."
  type        = string
}
