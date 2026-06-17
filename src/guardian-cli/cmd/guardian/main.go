// Command guardian is the controller-side bootstrap CLI. It turns a checked
// out workspace plus host/environment YAML into a converged cluster:
// provider reinstall into Talos, machine config, etcd bootstrap,
// workspace-built OCI artifacts pushed through the in-cluster seed registry,
// components applied.
// It is a client that watches control loops converge; it is not a resident
// daemon.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// errUsage marks a command-line misuse — a bad flag, flag combination, or
// argument count — so main can exit 2 (the conventional usage code, and what
// flag.ExitOnError uses). Every other error exits 1. Wrap it with %w.
var errUsage = errors.New("usage")

// Version pins live here, not in flags or env: changing what the fleet runs
// must be a reviewed commit.
const (
	talosVersion   = "v1.13.4"
	openbaoVersion = "v2.5.4"
	factoryURL     = "https://factory.talos.dev"
)

const usage = `usage:
  guardian up [--restore <file|url> --sha256 <hex>] [host.yaml]
                                     converge the node: machine config, etcd, seed registry, push artifacts, components;
                                     with --restore, force-restore OpenBao from the verified snapshot into a fresh vault
  guardian down --yes [host.yaml]
                                     wipe the node to Talos maintenance mode via talosctl reset
  guardian config [host <path>]      print config, or set the default host facts file
  guardian host list|inspect|use     inspect or select checked-in hosts
  guardian run <tool> [args...]      run a version-pinned tool (aspect, bazel, talosctl, kubectl, oras, cosign)
  guardian tools install|uninstall   manage tool symlinks pointing at this binary (--bin-dir, default ~/.local/bin)
  guardian version                   print pinned component versions`

func main() {
	// Installed as a tool symlink (`guardian tools install`), the binary is
	// invoked under the tool's name and acts as that pinned tool.
	if name := filepath.Base(os.Args[0]); isManagedTool(name) {
		if err := runNamedTool(name, os.Args[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "guardian run %s: %v\n", name, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "up":
		err = runUp(os.Args[2:])
	case "down":
		err = runDown(os.Args[2:])
	case "config":
		err = runConfigCmd(os.Args[2:])
	case "host":
		err = runHostCmd(os.Args[2:])
	case "run":
		err = runRunCmd(os.Args[2:])
	case "tools":
		err = runToolsCmd(os.Args[2:])
	case "version":
		fmt.Printf("talos\t%s\nopenbao\t%s\n", talosVersion, openbaoVersion)
	default:
		fmt.Fprintf(os.Stderr, "guardian: unknown command %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, errUsage) {
			fmt.Fprintf(os.Stderr, "guardian: %v\n%s\n", err, usage)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "guardian: %v\n", err)
		os.Exit(1)
	}
}
