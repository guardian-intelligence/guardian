# Directus migration monitor

`directus-migration-monitor` is a publishable, read-only SSH status checker for Directus and blog migrations. It accepts an existing SSH alias or hostname and never accepts an identity file, password, remote command, shell fragment, or configuration path.

```sh
directus-migration-monitor --host migration-host
```

The command forces batch public-key authentication, disables prompts and TTY allocation, clears SSH forwarding, and caps connection setup at ten seconds by default. It runs a fixed remote probe that reports counts for Directus systemd units, containers, processes, and Kubernetes workloads. It does not mutate the remote host, deploy resources, read configuration or credentials, or claim a migration succeeded when no workload is present.

Use `--timeout 1..60` to bound a slower existing route. The target must already be configured for non-interactive SSH authentication.

## Release posture

The package is intentionally `publish = false` until the crates.io name and its trusted-publisher registration are claimed. Its release pipeline must publish signed, checksumed release assets and a verified installer before the public install instructions are enabled.
