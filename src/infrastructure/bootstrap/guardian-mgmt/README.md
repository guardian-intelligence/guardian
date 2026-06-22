# guardian-mgmt OpenTofu root

Adopts the Latitude.sh bare-metal substrate for the `guardian-mgmt` control
plane into OpenTofu state:

- project `proj_R82A0yqmd06mM`
- VLAN `vlan_8mop5gkpP5jxv` / VID `2140`
- control-plane servers `ash-earth`, `ash-wind`, `ash-water`

Authentication comes from the Latitude provider standard environment variable.
OpenTofu state is stored in the R2 bucket/key declared in `versions.tf`; the S3
backend reads credentials and endpoint from `AWS_*` environment variables.

```sh
export LATITUDESH_AUTH_TOKEN=...
export AWS_ACCESS_KEY_ID="${cloudflare_r2_access_key_id}"
export AWS_SECRET_ACCESS_KEY="${cloudflare_r2_secret_access_key}"
export AWS_ENDPOINT_URL_S3="${cloudflare_r2_s3_api_endpoint}"
```

Use the repo-pinned OpenTofu binary through Aspect:

```sh
aspect infra init
aspect infra adopt-known
aspect infra plan
aspect infra output
```

The state object is useful, but not the sole authority. If it is lost, rerun the
imports from checked-in HCL and Latitude's API. The durable inputs are the
resource declarations, import IDs, shared inventory JSON, and provider lockfile
in this repo.

`adopt-known` imports only resources with stable IDs already captured in the
runbook. VLAN assignments also need import, but the provider import ID is
`<PROJECT_ID>:<VLAN_ASSIGNMENT_ID>`; the assignment IDs are not the server IDs or
the VLAN ID. Once those IDs are captured from Latitude, import them with:

```sh
aspect infra tofu --args 'import latitudesh_vlan_assignment.control_plane["ash-earth"] proj_R82A0yqmd06mM:<ASSIGNMENT_ID>'
aspect infra tofu --args 'import latitudesh_vlan_assignment.control_plane["ash-wind"] proj_R82A0yqmd06mM:<ASSIGNMENT_ID>'
aspect infra tofu --args 'import latitudesh_vlan_assignment.control_plane["ash-water"] proj_R82A0yqmd06mM:<ASSIGNMENT_ID>'
```

Do not run `apply` until the assignment resources are imported or deliberately
approved for creation. The server resources are adoption-guarded with
`allow_reinstall = false`, `prevent_destroy = true`, and ignored reinstall-only
fields.
