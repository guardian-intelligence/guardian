package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runUp converges the site node from whatever state it is in: maintenance
// mode gets machine config applied and etcd bootstrapped; a configured node
// gets the regenerated config re-applied. Both paths end with the seed
// registry up, every workspace-built component pushed into it by digest, and
// the component manifests applied.
func runUp(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // main is the single point that reports usage errors
	restoreRef := fs.String("restore", "", "restore OpenBao from a raft snapshot (file path or http(s) URL) before unseal; requires --sha256")
	wantSHA := fs.String("sha256", "", "expected hex sha256 of the --restore snapshot")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usage)
			return nil
		}
		return fmt.Errorf("up: %w: %v", errUsage, err)
	}
	restoring := *restoreRef != ""
	if restoring != (*wantSHA != "") {
		return fmt.Errorf("up: %w: --restore and --sha256 must be given together", errUsage)
	}
	site, sitePath, err := resolveSite(fs.Args())
	if err != nil {
		return fmt.Errorf("up: %w", err)
	}
	talosctl, err := talosctlPath()
	if err != nil {
		return err
	}
	kubectl, err := kubectlPath()
	if err != nil {
		return err
	}
	state, err := stateDir(site.Cluster.Name)
	if err != nil {
		return err
	}

	// Verify the restore blob before any node mutation: a corrupt or wrong
	// snapshot must fail while the cluster is still untouched, not after a
	// wipe-and-converge has already destroyed the old data.
	var snapPath string
	if restoring {
		snapPath, err = fetchAndVerifySnapshot(*restoreRef, *wantSHA, state)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "verified restore snapshot %s (sha256 %s)\n", *restoreRef, *wantSHA)
	}

	node := site.Node.Address

	id, err := registerSchematic(site.Talos.Schematic)
	if err != nil {
		return err
	}

	// Talos cluster secrets are generated once per site and reused across
	// wipe drills so talosconfig and cluster identity stay stable.
	secrets := filepath.Join(state, "secrets.yaml")
	if _, err := os.Stat(secrets); os.IsNotExist(err) {
		if err := runTool(talosctl, "gen", "secrets", "-o", secrets); err != nil {
			return err
		}
	}

	// Machine config is regenerated every run: it is a pure function of the
	// secrets bundle, the pinned installer image, and the checked-in patches.
	genArgs := []string{
		"gen", "config", site.Cluster.Name, site.Cluster.Endpoint,
		"--with-secrets", secrets,
		"--install-image", installerImage(id),
		"--output-types", "controlplane,talosconfig",
		"--output", state,
		"--force",
	}
	for _, p := range site.Talos.Patches {
		genArgs = append(genArgs, "--config-patch", "@"+resolvePath(p))
	}
	// Select the install disk by serial; diskSelector takes precedence over
	// the default install.disk path, so the ZFS disk is never a target.
	genArgs = append(genArgs, "--config-patch",
		fmt.Sprintf(`{"machine":{"install":{"diskSelector":{"serial":%q}}}}`, site.Node.InstallDiskSerial))
	// The site has no DHCP: static addressing comes from site.yaml, the same
	// facts `down` passes to the maintenance kernel. Without this patch the
	// installed node boots unreachable. The link is selected by MAC
	// (deviceSelector), never by name: a dangling name fails silently — the
	// platform fallback still assigns the address, but the routes drop and
	// the configured node boots network-dark.
	genArgs = append(genArgs, "--config-patch",
		fmt.Sprintf(`{"machine":{"network":{"hostname":%q,"interfaces":[{"deviceSelector":{"hardwareAddr":%q},"addresses":["%s/%d"],"routes":[{"network":"0.0.0.0/0","gateway":%q}]}]}}}`,
			site.Node.Hostname, site.Node.InterfaceMac, site.Node.Address, site.Node.PrefixLength, site.Node.Gateway))
	// gen config emits a HostnameConfig document (auto: stable) that fails
	// validation against the static v1alpha1 hostname above; delete it.
	genArgs = append(genArgs, "--config-patch", `{"apiVersion":"v1alpha1","kind":"HostnameConfig","$patch":"delete"}`)
	// Boot-time static addressing comes from the schematic's extraKernelArgs,
	// baked into the UKI the installer writes; machine.install.extraKernelArgs
	// must stay unset because it conflicts with grubUseUKICmdline (UKI mode),
	// which gen config enables by default.
	if err := runTool(talosctl, genArgs...); err != nil {
		return err
	}
	talosconfig := filepath.Join(state, "talosconfig")
	controlplane := filepath.Join(state, "controlplane.yaml")
	talosArgs := func(rest ...string) []string {
		return append([]string{"--talosconfig", talosconfig, "-n", node, "-e", node}, rest...)
	}

	// Probe runtime truth instead of trusting recorded state: a configured
	// node answers the authenticated API; a maintenance-mode node only
	// answers insecure requests. Poll because `up` may be invoked while the
	// node is mid-install or mid-reboot and briefly answers neither.
	var mode string
	err = poll("talos api (configured or maintenance)", 10*time.Minute, 10*time.Second, func() error {
		if _, verr := outputTool(talosctl, talosArgs("version", "--short")...); verr == nil {
			mode = "configured"
			return nil
		}
		if _, derr := outputTool(talosctl, "get", "disks", "--insecure", "-n", node); derr == nil {
			mode = "maintenance"
			return nil
		}
		return fmt.Errorf("node %s answers neither configured nor maintenance Talos API (run guardian down for a fresh node)", node)
	})
	if err != nil {
		return err
	}

	if mode == "configured" {
		fmt.Fprintln(os.Stderr, "node is configured; re-applying machine config")
		if err := runTool(talosctl, talosArgs("apply-config", "-f", controlplane)...); err != nil {
			return err
		}
	} else {
		disks, derr := outputTool(talosctl, "get", "disks", "--insecure", "-n", node)
		if derr != nil {
			return fmt.Errorf("up: maintenance disk inventory: %w", derr)
		}
		fmt.Fprint(os.Stderr, disks)
		// Refuse to install unless both serials are present: the install
		// serial must exist to target it, and the ZFS serial confirms this is
		// the intended two-NVMe box and not some other host.
		for label, serial := range map[string]string{"install": site.Node.InstallDiskSerial, "zfs": site.Node.ZFSDiskSerial} {
			if !strings.Contains(disks, serial) {
				return fmt.Errorf("up: %s disk serial %s not in node disk inventory above; fix node in %s", label, serial, sitePath)
			}
		}
		// Same bar for the NIC: the declared MAC must exist on the node, or
		// the deviceSelector would silently match nothing and the installed
		// system would boot without routes.
		links, lerr := outputTool(talosctl, "get", "links", "--insecure", "-n", node)
		if lerr != nil {
			return fmt.Errorf("up: maintenance link inventory: %w", lerr)
		}
		if !strings.Contains(strings.ToLower(links), strings.ToLower(site.Node.InterfaceMac)) {
			fmt.Fprint(os.Stderr, links)
			return fmt.Errorf("up: interface MAC %s not in node link inventory above; fix node in %s", site.Node.InterfaceMac, sitePath)
		}
		if err := runTool(talosctl, "apply-config", "--insecure", "-n", node, "-f", controlplane); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "machine config applied; node is installing to disk and rebooting")
	}

	// Bootstrap etcd on both paths so re-runs converge: the API answers
	// while install/reboot is still in flight (FailedPrecondition until the
	// installed system is up), and an already-bootstrapped node reports
	// AlreadyExists, which is success here.
	err = poll("etcd bootstrap", 20*time.Minute, 15*time.Second, func() error {
		out, berr := outputTool(talosctl, talosArgs("bootstrap")...)
		if berr == nil || strings.Contains(out, "AlreadyExists") {
			return nil
		}
		return berr
	})
	if err != nil {
		return err
	}

	kubeconfig := filepath.Join(state, "kubeconfig")
	err = poll("kubeconfig from talos", 10*time.Minute, 10*time.Second, func() error {
		_, kerr := outputTool(talosctl, talosArgs("kubeconfig", kubeconfig, "--force")...)
		return kerr
	})
	if err != nil {
		return err
	}
	err = poll("kubernetes node Ready", 15*time.Minute, 10*time.Second, func() error {
		out, kerr := outputTool(kubectl, "--kubeconfig", kubeconfig, "get", "nodes", "--no-headers")
		if kerr != nil {
			return kerr
		}
		if !strings.Contains(out, " Ready") {
			return fmt.Errorf("node not Ready yet:\n%s", out)
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Seed registry first: it is the transport for every other component.
	if err := runTool(kubectl, "--kubeconfig", kubeconfig, "apply", "-f", resolvePath(seedRegistryManifest)); err != nil {
		return err
	}
	if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "seed-registry", "rollout", "status", "deployment/seed-registry", "--timeout=10m"); err != nil {
		return err
	}

	// Push every workspace-built layout through the port-forward, then apply
	// manifests referencing the mirror name at the built digest.
	images := make(map[string]string, len(components))
	digests := make(map[string]string, len(components))
	err = withPortForward(kubectl, kubeconfig, "seed-registry", "deploy/seed-registry", pushLocalPort, 5000, 2*time.Minute, func(endpoint string) error {
		for _, c := range components {
			// Manifest-only components have nothing to push; site-gated
			// components push nothing for sites they do not converge on.
			if c.layout == "" || (c.enabled != nil && !c.enabled(site)) {
				continue
			}
			dir, terr := toolPath(c.layout)
			if terr != nil {
				return terr
			}
			digest, perr := pushLayout(dir, endpoint, c.name)
			if perr != nil {
				return perr
			}
			digests[c.name] = digest.String()
			images[c.name] = fmt.Sprintf("%s/%s@%s", mirrorHost, c.name, digest)
			fmt.Printf("pushed\t%s\n", images[c.name])
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, c := range components {
		if c.enabled != nil && !c.enabled(site) {
			fmt.Fprintf(os.Stderr, "skipping %s: disabled for this site\n", c.name)
			continue
		}
		// images[c.name] is "" for a manifest-only component; its template
		// never references .Image (the components-table test pins the gate,
		// the render test pins the manifest).
		manifest, rerr := renderComponentManifest(c, images[c.name], site)
		if rerr != nil {
			return rerr
		}
		// A component may guard its entire manifest on site values (status
		// renders nothing on sites without status.domains); kubectl rejects
		// empty input, so an empty render means "not deployed here".
		if len(bytes.TrimSpace(manifest)) == 0 {
			fmt.Fprintf(os.Stderr, "skipping %s: manifest renders empty for this site\n", c.name)
			continue
		}
		if err := runToolInput(manifest, kubectl, "--kubeconfig", kubeconfig, "apply", "-f", "-"); err != nil {
			return err
		}
	}
	// Mark the converge with a node-scoped Event — the k8s-native deploy
	// marker. Failure is a warning, never a converge failure: the event is a
	// marker, not substrate — etcd forgets it after an hour and nothing
	// durable depends on it before the ledger.
	if eerr := emitConvergedEvent(kubectl, kubeconfig, site.Node.Hostname, digests); eerr != nil {
		fmt.Fprintf(os.Stderr, "warning: converged event not recorded: %v\n", eerr)
	}
	if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "openbao", "rollout", "status", "statefulset/openbao", "--timeout=10m"); err != nil {
		return err
	}

	// Probe OpenBao's seal/init state over its HTTP API through a port-forward
	// (the bao CLI hits these same endpoints) and dispatch on probed truth
	// crossed with operator intent — never on recorded state, the same
	// philosophy as the Talos mode probe above. The dance, restore-over-data
	// refusal, and unseal guidance all live in this closure.
	baoExec := fmt.Sprintf("kubectl --kubeconfig %s -n openbao exec -it openbao-0 -- env BAO_ADDR=http://127.0.0.1:8200 bao", kubeconfig)
	err = withPortForward(kubectl, kubeconfig, "openbao", "pod/openbao-0", baoLocalPort, 8200, 2*time.Minute, func(addr string) error {
		st, herr := probeBaoHealth(addr)
		if herr != nil {
			return fmt.Errorf("up: %w (last: %v); inspect: kubectl --kubeconfig %s -n openbao logs openbao-0", errBaoUnreachable, herr, kubeconfig)
		}
		action, derr := baoDecision(st, restoring)
		switch {
		case errors.Is(derr, errRestoreOverData):
			return fmt.Errorf("up: %w: openbao is %s — restore is only legal into a fresh vault; wipe first:\n  guardian down --yes && guardian up --restore %s --sha256 %s",
				errRestoreOverData, st, *restoreRef, *wantSHA)
		case derr != nil:
			return fmt.Errorf("up: %w", derr)
		}
		if action == actRestore {
			if rerr := restoreSnapshot(addr, snapPath); rerr != nil {
				return rerr
			}
			fmt.Printf("\nrestored %s; unseal with the site's original shares:\n  %s operator unseal\n", *wantSHA, baoExec)
			return nil
		}
		switch st {
		case baoFresh:
			fmt.Println("\nopenbao is uninitialized. choose one:")
			fmt.Printf("  new site:  %s operator init\n", baoExec)
			fmt.Println("  recovery:  guardian up --restore <file|https URL> --sha256 <digest>")
		case baoSealed:
			fmt.Println("\nopenbao is initialized but sealed — unseal to resume:")
			fmt.Printf("  %s operator unseal\n", baoExec)
		case baoUnsealed:
			fmt.Println("\nopenbao is initialized and unsealed — healthy.")
		}
		return nil
	})
	if err != nil {
		return err
	}

	fmt.Printf("\nconverged: %s is up; kubeconfig at %s\n", site.Cluster.Name, kubeconfig)
	return nil
}

// emitConvergedEvent records one core/v1 Event per converge on the site's
// Node, message one name@digest line per component pushed this run. Node
// rather than per-workload involvedObject: plumbing the kind (Deployment vs
// StatefulSet) per component is not worth it when describe-node shows the
// marker and the ledger will capture all events anyway. Created via
// `kubectl create` because `apply` does not support generateName.
func emitConvergedEvent(kubectl, kubeconfig, nodeName string, digests map[string]string) error {
	lines := make([]string, 0, len(components))
	for _, c := range components {
		// The event documents image digests pushed this run; manifest-only
		// and site-disabled components pushed nothing and are omitted.
		if digests[c.name] == "" {
			continue
		}
		lines = append(lines, c.name+"@"+digests[c.name])
	}
	now := time.Now().UTC().Format(time.RFC3339)
	event := map[string]any{
		"apiVersion":         "v1",
		"kind":               "Event",
		"metadata":           map[string]string{"generateName": "converged-", "namespace": "default"},
		"involvedObject":     map[string]string{"kind": "Node", "name": nodeName},
		"reason":             "Converged",
		"message":            strings.Join(lines, "\n"),
		"type":               "Normal",
		"firstTimestamp":     now,
		"lastTimestamp":      now,
		"reportingComponent": "guardian",
		"source":             map[string]string{"component": "guardian"},
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("converged event: %w", err)
	}
	return runToolInput(raw, kubectl, "--kubeconfig", kubeconfig, "create", "-f", "-")
}
