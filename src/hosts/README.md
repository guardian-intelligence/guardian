# Hosts

`src/hosts` is the physical inventory layer. Each directory is a stable asset
ID, not an environment name and not a current Kubernetes node name.

```
src/hosts/
  ash-bm-001/
    host.yaml
    talos/
      schematic.yaml
      patches/
  ash-bm-002/
    host.yaml
  ash-bm-003/
    host.yaml
```

`host.yaml` records the facts `guardian up` needs before a Kubernetes API
exists: provider identity, assignment, static network facts, disk serials,
storage pools, and Talos inputs. Physical facts are copied from the machine or
provider API, never from another host.

The public commands are:

```bash
guardian host list
guardian host inspect src/hosts/ash-bm-001/host.yaml
guardian host use src/hosts/ash-bm-001/host.yaml
guardian down --yes
guardian up
```

`guardian host use` writes the selected host path to the operator-local config.
`guardian up` and `guardian down` also accept an explicit `host.yaml` path for
one-off drills.
