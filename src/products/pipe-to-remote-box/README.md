# Pipe to Remote Box

Pipe to Remote Box is a small, read-only command-line check-in for a directory
on a remote machine reached through an existing SSH configuration. You choose
both the host and an absolute directory; the tool sends one fixed probe and
prints a bounded status report.

```sh
pipe-to-remote-box --host my-box --directory /srv/project
```

It is useful when a long-running build, migration, export, or writing session
leaves its progress in a directory and you want a quick view without opening an
interactive shell.

## What it reports

For a directory that exists, the report includes:

- directory metadata available from the remote `stat` implementation;
- the number of immediate entries;
- at most 20 recently modified file paths, bounded to the directory and its
  direct subdirectories and rendered as hexadecimal path bytes; and
- when the directory has a non-symlink `.git` directory and `git` is available,
  the validated full `HEAD` revision and its commit time.

Recent paths are hexadecimal so newlines, bidirectional formatting characters,
invalid UTF-8 and other hostile filename bytes cannot forge report records or
terminal output. Decode a path only when you need it, for example with
`printf '%s' HEX | xxd -r -p`. Recent files are capped at 20 and each captured
SSH output stream at 256 KiB. Terminal controls, including Unicode
bidirectional controls, are escaped before output is written locally.

The probe deliberately does not run `git status`, a working-tree diff, text
conversion or external diff drivers. Those commands can execute programs
selected by repository-local Git configuration or attributes. Revision and
commit-time queries do not inspect file contents, and global and system Git
configuration are disabled for them.

The probe does not print file contents, read environment variables, enumerate
other directories, or accept a remote command. Missing directories are
reported as errors rather than silently inspecting a parent or fallback path.

Output is intentionally human-readable. Treat it as potentially sensitive:
the selected directory, encoded file paths, timestamps, entry counts and Git
revision can disclose names and activity after decoding. Terminal scrollback,
session logging or any redirection you choose may retain that output. The SSH
server controls the bytes returned by the session and can also record the
connection in its normal authentication and audit logs.

## Safety boundary

Use Pipe to Remote Box only on hosts and directories you own or are authorized
to inspect. It is not a discovery or access-bypass tool. It uses the normal
OpenSSH client configuration and authentication already available to the
current user; it does not accept passwords, private-key paths, SSH config
paths, or arbitrary SSH options.

Every invocation:

- requires an explicit host and absolute directory;
- rejects option-like hosts and unsafe directory syntax before starting SSH;
- enables batch mode and disables password and keyboard-interactive prompts;
- allocates no TTY and clears forwarding requests;
- applies one deadline to connection setup and the complete remote probe; and
- sends a constant POSIX-shell probe over standard input, with the validated
  directory as data rather than executable shell text.

The probe contains no write, create, delete, deployment or
privilege-escalation operation. As with any SSH session, server-side login
hooks, SSH accounting, filesystem access-time policy and administrator-added
shell instrumentation remain properties of the remote environment rather than
this program.

## Usage

```text
pipe-to-remote-box --host <HOST> --directory <ABSOLUTE_DIRECTORY> [--timeout <SECONDS>]
```

`--timeout` defaults to 10 seconds and accepts 1 through 60 seconds. It bounds
the entire SSH subprocess, not just TCP connection establishment. The selected
host must already support non-interactive public-key or SSH-agent
authentication.

Host aliases may use the routes in your existing `~/.ssh/config`, including a
deliberately configured jump host. OpenSSH configuration can itself invoke
local route helpers through `ProxyCommand`, `ProxyJump`, or `Match exec`; use
only configuration you trust. Pipe to Remote Box does not create or modify
that configuration, and it overrides prompting, forwarding, TTY, local-command,
connection-sharing, host-key-update and agent-state-write options for the
session it starts. A failed check returns non-zero and explains whether local
validation, SSH startup, the deadline, or the remote probe failed.

## Install

Four prebuilt targets are released:

- `x86_64-unknown-linux-musl`
- `aarch64-unknown-linux-musl`
- `x86_64-apple-darwin`
- `aarch64-apple-darwin`

Stable releases are distributed as GitHub Release assets, the
`@guardian-intelligence/pipe-to-remote-box` npm package, and the
`pipe-to-remote-box` crate. Until the first stable release and registry trusted
publishers exist, build from a reviewed checkout:

```sh
make
./target/release/pipe-to-remote-box --version
```

After the first stable release, select a product version explicitly when
fetching the installer. Repository-wide `releases/latest` is intentionally not
used because this repository publishes more than one product:

```sh
version=0.1.0
base="https://github.com/guardian-intelligence/guardian/releases/download/pipe-to-remote-box%2Fv$version"
curl -fsSL "$base/install.sh" | sh -s -- --version "$version"
```

Piping a network response into a shell trusts that response. The preferred
high-assurance path downloads `install.sh` and `install.sh.sigstore.json` from
the chosen release, verifies the bundle against the pinned release-workflow
identity documented in
[`docs/pipe-to-remote-box-distribution.md`](../../../docs/pipe-to-remote-box-distribution.md),
and then runs the verified file. The installer independently verifies the
selected binary's checksum and build-time Sigstore bundle before it replaces an
existing installation.

Package-manager installation, once the stable mirrors are active:

```sh
npm install --global @guardian-intelligence/pipe-to-remote-box
cargo install --locked pipe-to-remote-box
```

To build and install from source:

```sh
make
sudo make install
```

`make install` does not build under `sudo`; it installs the already-built
release binary. `make uninstall` removes only that conventional Makefile path.
Package-manager installations must be removed through their package manager.

## Platform requirements and limitations

The local machine needs OpenSSH. The remote account needs a POSIX `sh`, `wc`,
`head`, `tr`, `od`, `grep`, and a common GNU/BSD `find` that implements
`-mindepth` and `-maxdepth`. GNU or BSD `stat` adds the directory timestamp;
without either, that field is `unavailable`. Git revision reporting is
optional. Permissions can make otherwise valid paths or Git details
unavailable. The report is a point-in-time observation, not a synchronization
protocol or monitoring service, and no claim is made about work that does not
leave evidence inside the selected directory.

## License

Pipe to Remote Box is licensed under Apache-2.0. The complete license is in
[`LICENSE`](LICENSE), and required dependency notices are in
[`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md). The signed OCI artifact
carries `LICENSE`; GitHub Release assets, source archives, crates.io packages
and every staged npm package carry both files.
