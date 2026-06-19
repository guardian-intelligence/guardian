# Latitude fleet state

This stack is intentionally flat. It tracks existing Latitude bare-metal
servers and provider-side metadata only:

- server identity
- plan, site, project, billing, and OS-of-record
- provider hostname and tags
- `allow_reinstall`

It does not run Talos, Talm, Cozystack, Flux, Crossplane, or app deployment.
`guardian up` owns host come-up after a specific checked-in host asset is
selected.

## Current import

`ash-bm-001` already exists in Latitude as `sv_vAPXaMxKM5epz`. The import block
binds that remote object to `latitudesh_server.host["ash-bm-001"]`.

```bash
cd src/fleet/opentofu/latitude
export LATITUDESH_AUTH_TOKEN="$(tr -d '\n' </tmp/latitude.token)"
tofu init
tofu plan
```

Review the plan before applying. The first clean import may show intentional
drift from the old Verself name toward the Guardian asset hostname.

Do not commit `.tfstate`, plan files, or `.terraform/`. Before this stack is
used by more than one operator or for prod writes, move state to encrypted,
versioned remote storage with locking.
