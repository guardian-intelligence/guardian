# Pipe to Remote Box

`pipe-to-remote-box` is a publishable, read-only SSH status checker for an explicitly selected remote directory. It accepts neither an identity file, password, remote command, nor shell fragment.

```sh
pipe-to-remote-box --host remote-box --directory /srv/writing
```

The command forces batch public-key authentication, disables prompts and TTY allocation, clears SSH forwarding, and caps connection setup at ten seconds by default. It runs a fixed remote probe that reports directory presence, metadata, entry count, up to twenty recently modified files, and Git status plus latest history when the directory is a checkout. It does not mutate the remote host, deploy resources, read configuration or credentials, or execute caller-supplied remote code.

Use `--timeout 1..60` to bound a slower existing route. The target must already be configured for non-interactive SSH authentication.

## Release posture

The package is intentionally `publish = false` until the crates.io name and its trusted-publisher registration are claimed. Its release pipeline must publish signed, checksumed release assets and a verified installer before the public install instructions are enabled.
