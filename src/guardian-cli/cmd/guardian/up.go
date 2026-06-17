package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"text/template"
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
		patchPath, err := resolveRepoInputPath(p)
		if err != nil {
			return err
		}
		genArgs = append(genArgs, "--config-patch", "@"+patchPath)
	}
	// Select the install disk by serial; diskSelector takes precedence over
	// the default install.disk path, so the ZFS disk is never a target.
	genArgs = append(genArgs, "--config-patch",
		fmt.Sprintf(`{"machine":{"install":{"diskSelector":{"serial":%q}}}}`, site.Node.InstallDiskSerial))
	// The site has no DHCP: static addressing comes from bootstrap.yaml, the same
	// facts `down` passes to the maintenance kernel. Without this patch the
	// installed node boots unreachable. The link is selected by MAC
	// (deviceSelector), never by name: a dangling name fails silently — the
	// platform fallback still assigns the address, but the routes drop and
	// the configured node boots network-dark.
	genArgs = append(genArgs, "--config-patch",
		fmt.Sprintf(`{"machine":{"network":{"hostname":%q,"interfaces":[{"deviceSelector":{"hardwareAddr":%q},"addresses":["%s/%d"],"routes":[{"network":"0.0.0.0/0","gateway":%q}]}]}}}`,
			site.Node.Hostname, site.Node.InterfaceMac, site.Node.Address, site.Node.PrefixLength, site.Node.Gateway))
	if siteUsesLocalStorage(site) {
		genArgs = append(genArgs, "--config-patch",
			fmt.Sprintf(`{"machine":{"kubelet":{"extraMounts":[{"destination":%q,"type":"bind","source":%q,"options":["rbind","rshared","rw"]}]}}}`,
				talosKubeletStorageRoot, talosKubeletStorageRoot))
	}
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

	if err := restorePlatformTLSSecrets(kubectl, kubeconfig, state, site); err != nil {
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
			if len(c.imageLayouts()) == 0 || (c.enabled != nil && !c.enabled(site)) {
				continue
			}
			for _, img := range c.imageLayouts() {
				dir, terr := toolPath(img.layout)
				if terr != nil {
					return terr
				}
				digest, perr := pushLayout(dir, endpoint, img.name)
				if perr != nil {
					return perr
				}
				digests[img.name] = digest.String()
				images[img.name] = fmt.Sprintf("%s/%s@%s", mirrorHost, img.name, digest)
				fmt.Printf("pushed\t%s\n", images[img.name])
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	apply := func(name string) error {
		c, ok := lookupComponent(name)
		if !ok {
			return fmt.Errorf("component %s missing from components table", name)
		}
		return applyComponent(kubectl, kubeconfig, c, images, site)
	}
	if err := resetLocalStorageBootstrap(kubectl, kubeconfig, site); err != nil {
		return err
	}
	if err := apply("local-storage-bootstrap"); err != nil {
		return err
	}
	if err := waitLocalStorageBootstrap(kubectl, kubeconfig, site); err != nil {
		return err
	}
	if err := restartKubeletAfterLocalStorageBootstrap(talosctl, talosArgs, kubectl, kubeconfig, site); err != nil {
		return err
	}
	if err := apply("openbao"); err != nil {
		return err
	}
	if err := waitOpenBao(kubectl, kubeconfig); err != nil {
		return err
	}
	if err := reconcileOpenBao(kubectl, kubeconfig, site, restoring, *restoreRef, *wantSHA, snapPath); err != nil {
		return err
	}
	if err := apply("crossplane"); err != nil {
		return err
	}
	if err := waitCrossplane(kubectl, kubeconfig); err != nil {
		return err
	}
	if siteUsesPlatformTLS(site) {
		if err := apply("cert-manager"); err != nil {
			return err
		}
	}
	if err := apply("provider-kubernetes"); err != nil {
		return err
	}
	if err := waitProviderKubernetes(kubectl, kubeconfig); err != nil {
		return err
	}
	if err := apply("provider-kubernetes-config"); err != nil {
		return err
	}
	if err := apply("edge-gateway-platform"); err != nil {
		return err
	}
	if err := apply("secret-projection-platform"); err != nil {
		return err
	}
	if err := apply("storage-plane-platform"); err != nil {
		return err
	}
	if err := apply("public-http-service-platform"); err != nil {
		return err
	}
	if err := apply("directus-platform"); err != nil {
		return err
	}
	if err := apply("observability-stack-platform"); err != nil {
		return err
	}
	if err := apply("slo-profile-platform"); err != nil {
		return err
	}
	if err := apply("status-surface-platform"); err != nil {
		return err
	}
	if err := apply("oci-registry-platform"); err != nil {
		return err
	}
	if err := waitGuardianPlatform(kubectl, kubeconfig); err != nil {
		return err
	}
	if err := apply("aisucks-product-api"); err != nil {
		return err
	}
	if err := apply("company-site-product-api"); err != nil {
		return err
	}
	if err := waitGuardianProducts(kubectl, kubeconfig); err != nil {
		return err
	}
	if err := applyEnvironmentBundle(kubectl, kubeconfig, site, images); err != nil {
		return err
	}
	if err := waitEdgeGateway(kubectl, kubeconfig, site); err != nil {
		return err
	}
	if err := backupPlatformTLSSecrets(kubectl, kubeconfig, state, site); err != nil {
		return err
	}
	if err := apply("external-secrets"); err != nil {
		return err
	}
	if err := waitExternalSecrets(kubectl, kubeconfig); err != nil {
		return err
	}
	if err := waitSecretProjections(kubectl, kubeconfig, site); err != nil {
		return err
	}
	if err := waitEnvironmentCapabilities(kubectl, kubeconfig, site); err != nil {
		return err
	}
	for _, c := range components {
		switch c.name {
		case "openbao", "crossplane", "cert-manager", "provider-kubernetes", "provider-kubernetes-config", "edge-gateway-platform", "secret-projection-platform", "storage-plane-platform", "public-http-service-platform", "directus-platform", "observability-stack-platform", "slo-profile-platform", "status-surface-platform", "oci-registry-platform", "aisucks-product-api", "company-site-product-api", "external-secrets", "local-storage-bootstrap":
			continue
		default:
			if err := applyComponent(kubectl, kubeconfig, c, images, site); err != nil {
				return err
			}
		}
	}
	// Mark the converge with a node-scoped Event — the k8s-native deploy
	// marker. Failure is a warning, never a converge failure: the event is a
	// marker, not substrate — etcd forgets it after an hour and nothing
	// durable depends on it before the ledger.
	if eerr := emitConvergedEvent(kubectl, kubeconfig, site.Node.Hostname, digests); eerr != nil {
		fmt.Fprintf(os.Stderr, "warning: converged event not recorded: %v\n", eerr)
	}
	fmt.Printf("\nconverged: %s is up; kubeconfig at %s\n", site.Cluster.Name, kubeconfig)
	return nil
}

func lookupComponent(name string) (component, bool) {
	for _, c := range components {
		if c.name == name {
			return c, true
		}
	}
	return component{}, false
}

func applyEnvironmentBundle(kubectl, kubeconfig string, site *Site, images map[string]string) error {
	if len(bytes.TrimSpace(site.EnvironmentBundle.Raw)) == 0 {
		return fmt.Errorf("environment bundle %s is empty", site.EnvironmentBundle.Path)
	}
	rendered, err := renderEnvironmentBundle(site, images)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "applying environment bundle %s\n", site.EnvironmentBundle.Path)
	if err := runToolInput(rendered, kubectl, "--kubeconfig", kubeconfig, "apply", "-f", "-"); err != nil {
		return fmt.Errorf("apply environment bundle %s: %w", site.EnvironmentBundle.Path, err)
	}
	return nil
}

func renderEnvironmentBundle(site *Site, images map[string]string) ([]byte, error) {
	tmpl, err := template.New("environment").Option("missingkey=error").Parse(string(site.EnvironmentBundle.Raw))
	if err != nil {
		return nil, fmt.Errorf("render environment bundle %s: %w", site.EnvironmentBundle.Path, err)
	}
	var buf bytes.Buffer
	data := struct {
		Site   *Site
		Images map[string]string
	}{Site: site, Images: images}
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("render environment bundle %s: %w", site.EnvironmentBundle.Path, err)
	}
	return buf.Bytes(), nil
}

func readRepoManifest(path string) ([]byte, error) {
	resolved, err := resolveRepoInputPath(path)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("read site manifest %s: %w", resolved, err)
	}
	return raw, nil
}

func resolveRepoInputPath(path string) (string, error) {
	resolved := resolvePath(path)
	if _, err := os.Stat(resolved); err == nil {
		return resolved, nil
	} else if !os.IsNotExist(err) || filepath.IsAbs(path) {
		return "", fmt.Errorf("resolve repo input %s: %w", resolved, err)
	}
	runfile, rerr := toolPath("_main/" + path)
	if rerr != nil {
		return "", fmt.Errorf("resolve repo input %s: %w", resolved, rerr)
	}
	if _, err := os.Stat(runfile); err != nil {
		return "", fmt.Errorf("resolve repo input %s: %w", runfile, err)
	}
	return runfile, nil
}

func applyComponent(kubectl, kubeconfig string, c component, images map[string]string, site *Site) error {
	if c.enabled != nil && !c.enabled(site) {
		fmt.Fprintf(os.Stderr, "skipping %s: disabled for this site\n", c.name)
		return nil
	}
	if c.pushOnly {
		fmt.Fprintf(os.Stderr, "skipping %s: image is consumed by the environment bundle\n", c.name)
		return nil
	}
	// images[c.name] is "" for a manifest-only component; its template
	// never references .Image (the components-table test pins the gate,
	// the render test pins the manifest).
	manifest, rerr := renderComponentManifest(c, images[c.name], images, site)
	if rerr != nil {
		return rerr
	}
	// A component may guard its entire manifest on environment values; kubectl
	// rejects empty input, so an empty render means "not deployed here".
	if len(bytes.TrimSpace(manifest)) == 0 {
		fmt.Fprintf(os.Stderr, "skipping %s: manifest renders empty for this site\n", c.name)
		return nil
	}
	applyArgs := []string{"--kubeconfig", kubeconfig, "apply", "-f", "-"}
	if c.name == "external-secrets" || c.name == "crossplane" {
		// ESO CRDs are large enough that client-side apply's
		// last-applied-configuration annotation exceeds Kubernetes' 256 KiB
		// annotation limit. Crossplane's v2 CRDs have the same shape.
		// Server-side apply avoids that annotation.
		applyArgs = []string{"--kubeconfig", kubeconfig, "apply", "--server-side=true", "-f", "-"}
	}
	if err := runToolInput(manifest, kubectl, applyArgs...); err != nil {
		return err
	}
	if c.name == "cert-manager" {
		for _, deploy := range []string{"cert-manager", "cert-manager-cainjector", "cert-manager-webhook"} {
			if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "cert-manager", "rollout", "status", "deployment/"+deploy, "--timeout=5m"); err != nil {
				return err
			}
		}
		if err := applyCloudflareDNSTokenSecret(kubectl, kubeconfig); err != nil {
			return err
		}
	}
	return nil
}

func waitOpenBao(kubectl, kubeconfig string) error {
	return runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "openbao", "rollout", "status", "statefulset/openbao", "--timeout=10m")
}

func waitLocalStorageBootstrap(kubectl, kubeconfig string, site *Site) error {
	if !siteUsesLocalStorage(site) {
		return nil
	}
	return runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "guardian-storage", "wait", "--for=condition=complete", "job/zfs-pool-init", "--timeout=5m")
}

func restartKubeletAfterLocalStorageBootstrap(talosctl string, talosArgs func(...string) []string, kubectl, kubeconfig string, site *Site) error {
	if !siteUsesLocalStorage(site) {
		return nil
	}
	fmt.Fprintln(os.Stderr, "restarting kubelet so local ZFS mounts are visible to kubelet")
	if err := runTool(talosctl, talosArgs("service", "kubelet", "restart")...); err != nil {
		return err
	}
	return poll("kubernetes node Ready after local storage bootstrap", 5*time.Minute, 5*time.Second, func() error {
		out, kerr := outputTool(kubectl, "--kubeconfig", kubeconfig, "get", "nodes", "--no-headers")
		if kerr != nil {
			return kerr
		}
		if !strings.Contains(out, " Ready") {
			return fmt.Errorf("node not Ready yet:\n%s", out)
		}
		return nil
	})
}

func resetLocalStorageBootstrap(kubectl, kubeconfig string, site *Site) error {
	if !siteUsesLocalStorage(site) {
		return nil
	}
	if _, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "get", "namespace", "guardian-storage"); err != nil {
		return nil
	}
	return runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "guardian-storage", "delete", "job/zfs-pool-init", "--ignore-not-found=true")
}

func waitExternalSecrets(kubectl, kubeconfig string) error {
	for _, crd := range []string{
		"clustersecretstores.external-secrets.io",
		"externalsecrets.external-secrets.io",
		"secretstores.external-secrets.io",
	} {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Established", "crd/"+crd, "--timeout=2m"); err != nil {
			return err
		}
	}
	for _, deploy := range []string{"external-secrets", "external-secrets-webhook", "external-secrets-cert-controller"} {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "external-secrets", "rollout", "status", "deployment/"+deploy, "--timeout=5m"); err != nil {
			return err
		}
	}
	return nil
}

func waitCrossplane(kubectl, kubeconfig string) error {
	for _, crd := range []string{
		"compositeresourcedefinitions.apiextensions.crossplane.io",
		"compositions.apiextensions.crossplane.io",
		"environmentconfigs.apiextensions.crossplane.io",
		"functions.pkg.crossplane.io",
		"providers.pkg.crossplane.io",
	} {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Established", "crd/"+crd, "--timeout=2m"); err != nil {
			return err
		}
	}
	for _, deploy := range []string{"crossplane", "crossplane-rbac-manager"} {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "crossplane-system", "rollout", "status", "deployment/"+deploy, "--timeout=5m"); err != nil {
			return err
		}
	}
	return nil
}

func waitProviderKubernetes(kubectl, kubeconfig string) error {
	for _, pkg := range []string{
		"providers.pkg.crossplane.io/provider-kubernetes",
		"functions.pkg.crossplane.io/function-go-templating",
		"functions.pkg.crossplane.io/function-environment-configs",
		"functions.pkg.crossplane.io/function-auto-ready",
	} {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Healthy", pkg, "--timeout=5m"); err != nil {
			return err
		}
	}
	for _, crd := range []string{
		"objects.kubernetes.crossplane.io",
		"providerconfigs.kubernetes.crossplane.io",
	} {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Established", "crd/"+crd, "--timeout=2m"); err != nil {
			return err
		}
	}
	return nil
}

func waitGuardianPlatform(kubectl, kubeconfig string) error {
	for _, xrd := range []string{
		"edgegateways.platform.guardian.dev",
		"secretprojections.platform.guardian.dev",
		"storageplanes.platform.guardian.dev",
		"publichttpservices.platform.guardian.dev",
		"directusinstances.platform.guardian.dev",
		"observabilitystacks.platform.guardian.dev",
		"sloprofiles.platform.guardian.dev",
		"syntheticchecks.platform.guardian.dev",
		"statussurfaces.platform.guardian.dev",
		"ociregistries.platform.guardian.dev",
	} {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Established", "compositeresourcedefinition/"+xrd, "--timeout=2m"); err != nil {
			return err
		}
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Established", "crd/"+xrd, "--timeout=2m"); err != nil {
			return err
		}
	}
	return nil
}

func waitGuardianProducts(kubectl, kubeconfig string) error {
	for _, xrd := range []string{
		"aisucksproducts.products.guardian.dev",
		"companysites.products.guardian.dev",
	} {
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Established", "compositeresourcedefinition/"+xrd, "--timeout=2m"); err != nil {
			return err
		}
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Established", "crd/"+xrd, "--timeout=2m"); err != nil {
			return err
		}
	}
	return nil
}

func waitEdgeGateway(kubectl, kubeconfig string, site *Site) error {
	if !siteUsesEdgeGateway(site) {
		return nil
	}
	objects := []string{
		"edge-gateway-namespace",
		"edge-gateway-class",
		"edge-gateway",
	}
	if siteUsesPlatformTLS(site) {
		objects = append(objects, "edge-gateway-clusterissuer")
		certs, err := edgeGatewayCertificateObjectNames(site)
		if err != nil {
			return err
		}
		objects = append(objects, certs...)
	}
	for _, obj := range objects {
		if err := poll("provider-kubernetes object "+obj, 3*time.Minute, 2*time.Second, func() error {
			_, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "get", "objects.kubernetes.crossplane.io/"+obj)
			return err
		}); err != nil {
			return err
		}
		if err := runTool(kubectl, "--kubeconfig", kubeconfig, "wait", "--for=condition=Ready", "objects.kubernetes.crossplane.io/"+obj, "--timeout=3m"); err != nil {
			return err
		}
	}
	if siteUsesPlatformTLS(site) {
		certs, err := edgeGatewayCertificateTargets(site)
		if err != nil {
			return err
		}
		for _, cert := range certs {
			if err := waitEdgeGatewayCertificate(kubectl, kubeconfig, cert); err != nil {
				return err
			}
		}
	}
	return runTool(kubectl, "--kubeconfig", kubeconfig, "-n", "gateway", "get", "gateway", "edge")
}

func waitEdgeGatewayCertificate(kubectl, kubeconfig string, cert edgeGatewayCertificateTarget) error {
	certResource := "certificate.cert-manager.io/" + cert.name
	if err := poll(certResource, 3*time.Minute, 2*time.Second, func() error {
		_, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", cert.namespace, "get", certResource)
		return err
	}); err != nil {
		return err
	}
	return poll(certResource+" ready or public TLS verified", 10*time.Minute, 5*time.Second, func() error {
		status, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", cert.namespace, "get", certResource, "-o", "jsonpath={.status.conditions[?(@.type==\"Ready\")].status}")
		if err != nil {
			return err
		}
		if strings.TrimSpace(status) == "True" {
			return nil
		}
		if len(cert.dnsNames) == 0 {
			return fmt.Errorf("%s is not Ready", certResource)
		}
		var failures []string
		for _, dnsName := range cert.dnsNames {
			if err := verifyPublicTLSName(dnsName); err != nil {
				failures = append(failures, dnsName+": "+err.Error())
			}
		}
		if len(failures) == 0 {
			fmt.Fprintf(os.Stderr, "%s is not Ready; public TLS verifies for %s\n", certResource, strings.Join(cert.dnsNames, ", "))
			return nil
		}
		return fmt.Errorf("%s is not Ready; public TLS verification failed: %s", certResource, strings.Join(failures, "; "))
	})
}

func verifyPublicTLSName(dnsName string) error {
	dnsName = strings.TrimSuffix(strings.TrimSpace(dnsName), ".")
	if dnsName == "" {
		return fmt.Errorf("empty DNS name")
	}
	if strings.Contains(dnsName, "*") {
		return fmt.Errorf("wildcard DNS name cannot be probed directly")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", net.JoinHostPort(dnsName, "443"), &tls.Config{
		ServerName: dnsName,
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return err
	}
	return conn.Close()
}

func reconcileOpenBao(kubectl, kubeconfig string, site *Site, restoring bool, restoreRef, wantSHA, snapPath string) error {
	// Probe OpenBao's seal/init state over its HTTP API through a port-forward
	// (the bao CLI hits these same endpoints) and dispatch on probed truth
	// crossed with operator intent — never on recorded state, the same
	// philosophy as the Talos mode probe above.
	baoExec := fmt.Sprintf("kubectl --kubeconfig %s -n openbao exec -it openbao-0 -- env BAO_ADDR=http://127.0.0.1:8200 bao", kubeconfig)
	return withPortForward(kubectl, kubeconfig, "openbao", "pod/openbao-0", baoLocalPort, 8200, 2*time.Minute, func(addr string) error {
		st, herr := probeBaoHealth(addr)
		if herr != nil {
			return fmt.Errorf("up: %w (last: %v); inspect: kubectl --kubeconfig %s -n openbao logs openbao-0", errBaoUnreachable, herr, kubeconfig)
		}
		action, derr := baoDecision(st, restoring)
		switch {
		case errors.Is(derr, errRestoreOverData):
			return fmt.Errorf("up: %w: openbao is %s — restore is only legal into a fresh vault; wipe first:\n  guardian down --yes && guardian up --restore %s --sha256 %s",
				errRestoreOverData, st, restoreRef, wantSHA)
		case derr != nil:
			return fmt.Errorf("up: %w", derr)
		}
		if action == actRestore {
			if rerr := restoreSnapshot(addr, snapPath); rerr != nil {
				return rerr
			}
			st = baoSealed
			fmt.Fprintf(os.Stderr, "restored OpenBao snapshot %s; unsealing restored vault\n", wantSHA)
		}
		switch st {
		case baoFresh:
			initResp, ierr := initFreshBao(addr)
			if ierr != nil {
				return ierr
			}
			if uerr := unsealBao(addr, initResp.KeysB64); uerr != nil {
				return uerr
			}
			if cerr := configureBaoForProjection(addr, initResp.RootToken, site, true); cerr != nil {
				return cerr
			}
			fmt.Fprintln(os.Stderr, "initialized, unsealed, and configured fresh OpenBao")
		case baoSealed:
			keys := openBaoUnsealKeysFromEnv()
			if len(keys) == 0 {
				return fmt.Errorf("up: openbao is sealed; set %s or %s for unattended unseal, or run:\n  %s operator unseal", baoUnsealKeyEnv, baoUnsealKeysEnv, baoExec)
			}
			if uerr := unsealBao(addr, keys); uerr != nil {
				return uerr
			}
			token, source, terr := lookupBaoRootToken(os.Getenv, resolvePath("secret.env"))
			if terr != nil {
				return fmt.Errorf("read secret.env: %w", terr)
			}
			if token != "" {
				if cerr := configureBaoForProjection(addr, token, site, allowBaoSecretMigrationFromEnv()); cerr != nil {
					return cerr
				}
				fmt.Fprintf(os.Stderr, "verified OpenBao projection configuration using token from %s\n", source)
			}
			fmt.Fprintln(os.Stderr, "unsealed OpenBao")
		case baoUnsealed:
			token, source, terr := lookupBaoRootToken(os.Getenv, resolvePath("secret.env"))
			if terr != nil {
				return fmt.Errorf("read secret.env: %w", terr)
			}
			if token != "" {
				if cerr := configureBaoForProjection(addr, token, site, allowBaoSecretMigrationFromEnv()); cerr != nil {
					return cerr
				}
				fmt.Fprintf(os.Stderr, "verified OpenBao projection configuration using token from %s\n", source)
			} else {
				fmt.Fprintln(os.Stderr, "openbao is initialized and unsealed")
			}
		}
		return nil
	})
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
