# cloudflare-dns OpenTofu root

Declares the public DNS records that publish the management cluster's ingress
surfaces. Desired record names come from
`src/infrastructure/inventory/guardian-mgmt.json`; target A-record IPs are
derived from `nodes[*].public_ipv4` in that same inventory so the node list is
the only checked-in public-IP source.

Authentication uses the Cloudflare provider's standard environment variable.
OpenTofu state is stored in the R2 bucket/key declared in `versions.tf`; the S3
backend reads credentials and endpoint from `AWS_*` environment variables.

```sh
export CLOUDFLARE_API_TOKEN="${cloudflare_guardian_intelligence_org_dnz_zone_api_token}"
export AWS_ACCESS_KEY_ID="${cloudflare_r2_access_key_id}"
export AWS_SECRET_ACCESS_KEY="${cloudflare_r2_secret_access_key}"
export AWS_ENDPOINT_URL_S3="${cloudflare_r2_s3_api_endpoint}"
```

Use the repo-pinned OpenTofu binary through Aspect:

```sh
aspect infra dns-init
aspect infra dns-adopt-known
aspect infra dns-plan
aspect infra dns-apply
aspect infra dns-output
```

Do not run `dns-apply` until `dns-adopt-known` has imported the existing DNS
records listed in `.aspect/tasks/infra.axl` and the resulting plan has been
reviewed. At the time this root was added, the apex and
`oci.guardianintelligence.org` still pointed at the excluded Verself prod IP, so
their planned changes are an intentional traffic move rather than a formatting
cleanup.

`aspect infra management-converge-run` runs `dns-apply` by default after the
Kubernetes base, secret contracts, company-site publication, rollout checks, and
Talos health checks pass. Use `--skip-dns` for a deliberate staged run where
public traffic must not move yet.
