package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// runDown takes the site node to Talos maintenance mode with a wiped system
// disk. Nodes are guaranteed to start life with Talos (provisioned via the
// provider's iPXE pointed at the factory schematic), so a node that answers
// neither Talos API is an error, not a migration case. `guardian up`
// converges from the postcondition.
func runDown(args []string) error {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // main is the single point that reports usage errors
	yes := fs.Bool("yes", false, "acknowledge that this wipes the node's system disk")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usage)
			return nil
		}
		return fmt.Errorf("down: %w: %v", errUsage, err)
	}
	site, _, err := resolveSite(fs.Args())
	if err != nil {
		return fmt.Errorf("down: %w", err)
	}
	state, err := stateDir(site.Cluster.Name)
	if err != nil {
		return err
	}

	// Best-effort: if the node still answers and OpenBao holds data, fold that
	// into the warning so the operator sees what the wipe destroys. The probe
	// must never block the wipe — the hourly drill runs `down --yes`
	// unattended — so any failure degrades silently to the bare message.
	warn := baoWipeWarning(state)

	if !*yes {
		return fmt.Errorf("down: this wipes %s (%s) and everything running on it.\n%sre-run with --yes",
			site.Node.Hostname, site.Node.Address, warn)
	}
	if warn != "" {
		fmt.Fprint(os.Stderr, warn)
	}

	kubectl, kerr := kubectlPath()
	if kerr == nil {
		kubeconfig := filepath.Join(state, "kubeconfig")
		if _, err := os.Stat(kubeconfig); err == nil {
			if err := backupPlatformTLSSecrets(kubectl, kubeconfig, state, site); err != nil {
				fmt.Fprintf(os.Stderr, "warning: platform TLS survival backup skipped: %v\n", err)
			}
		}
	}

	talosctl, err := talosctlPath()
	if err != nil {
		return err
	}
	node := site.Node.Address
	talosconfig := filepath.Join(state, "talosconfig")

	maintenance := func() error {
		_, perr := outputTool(talosctl, "get", "disks", "--insecure", "-n", node)
		return perr
	}

	switch {
	case maintenance() == nil:
		fmt.Fprintln(os.Stderr, "node is already in maintenance mode")
		return nil
	case func() bool {
		_, verr := outputTool(talosctl, "--talosconfig", talosconfig, "-n", node, "-e", node, "version", "--short")
		return verr == nil
	}():
		// Wipe exactly STATE and EPHEMERAL: machine config and data are gone,
		// the installed OS and bootloader survive, and the node reboots into
		// maintenance mode. The default --wipe-mode all erases the entire
		// system disk (no bootloader left) and the user disks (the ZFS pool).
		// graceful=false because a single-node etcd cannot leave its own
		// cluster.
		fmt.Fprintln(os.Stderr, "node runs configured Talos; resetting")
		if err := runTool(talosctl, "--talosconfig", talosconfig, "-n", node, "-e", node,
			"reset", "--graceful=false", "--reboot", "--wait=false",
			"--system-labels-to-wipe", "STATE,EPHEMERAL"); err != nil {
			return err
		}
	default:
		id, serr := registerSchematic(site.Talos.Schematic)
		if serr != nil {
			return fmt.Errorf("down: node %s answers neither Talos API; %w", node, serr)
		}
		return fmt.Errorf("down: node %s answers neither Talos API; provision it with the provider's iPXE pointed at https://pxe.factory.talos.dev/pxe/%s/%s/metal-amd64",
			node, id, talosVersion)
	}

	if err := poll("talos maintenance mode on "+node, 20*time.Minute, 15*time.Second, maintenance); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "node is in maintenance mode; run: guardian up")
	return nil
}

// baoWipeWarning best-effort reports OpenBao's state for down's refusal/warning
// text. It returns "" whenever it can't tell — no kubeconfig yet, node down,
// half-converged — so down degrades to the bare wipe message and never blocks
// on the probe. The port-forward readiness is bounded tight (8s) so an
// unreachable OpenBao can't stall the unattended drill.
func baoWipeWarning(state string) string {
	kubeconfig := filepath.Join(state, "kubeconfig")
	if _, err := os.Stat(kubeconfig); err != nil {
		return ""
	}
	kubectl, err := kubectlPath()
	if err != nil {
		return ""
	}
	var st baoState
	ok := false
	_ = withPortForward(kubectl, kubeconfig, "openbao", "pod/openbao-0", baoLocalPort, 8200, 8*time.Second, func(addr string) error {
		s, e := baoHealth(addr)
		if e != nil {
			return e
		}
		st, ok = s, true
		return nil
	})
	if !ok {
		return ""
	}
	switch st {
	case baoSealed, baoUnsealed:
		return "  OpenBao is " + st.String() + " — this wipe destroys its data, unrecoverable\n  unless a snapshot exists offsite.\n"
	case baoFresh:
		return "  OpenBao is uninitialized — no secret data to lose.\n"
	default:
		return ""
	}
}
