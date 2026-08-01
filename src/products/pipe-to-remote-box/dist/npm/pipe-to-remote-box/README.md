# @guardian-intelligence/pipe-to-remote-box

Pipe to Remote Box is a read-only status check for an explicitly selected
directory on a remote machine reached through your existing SSH configuration.

```sh
npm install --global @guardian-intelligence/pipe-to-remote-box
pipe-to-remote-box --host my-box --directory /srv/project
```

The package has no install or lifecycle script. npm selects one of four
OS/architecture packages through `optionalDependencies`, and this package's
small Node shim executes its bundled native binary.

Use the tool only on hosts and directories you own or are authorized to
inspect. Its output can disclose hex-encoded file paths, Git revisions and
timestamps, even though it never prints file contents. See the
[source README](https://github.com/guardian-intelligence/guardian/tree/main/src/products/pipe-to-remote-box)
for the complete SSH safety boundary and output contract.

The native binary is byte-identical to the corresponding GitHub Release asset.
Before npm publication, the release lane verifies its checksum and Sigstore
bundle against this build identity:

```text
https://github.com/guardian-intelligence/guardian/.github/workflows/pipe-to-remote-box-image.yml@refs/heads/main
```

The full Apache-2.0 license and required third-party notices are included in
the published package.
