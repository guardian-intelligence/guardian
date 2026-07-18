// converged is the platform convergence proof: it verifies that every
// declared Flux Kustomization is Ready at the expected Git revision and
// nothing more. Workload and component health gate Kustomization readiness
// through Flux health checks declared in the manifests themselves
// (src/infrastructure/base/flux/sync.yaml), so this command stays a reader
// over Flux's own conditions rather than a second check engine.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var defaultKustomizations = []string{
	"guardian-mgmt-platform",
	"guardian-mgmt-platform-patches",
	"guardian-mgmt-storage",
	"guardian-mgmt-base",
	"guardian-mgmt-admission",
	"guardian-mgmt-app-patches",
	"guardian-vlogs-hardening-prerequisites",
	"guardian-vlogs-hardening",
	"guardian-system",
	"guardian-authorization-operator",
	"guardian-authorization-data",
	"guardian-authorization-prod",
	"guardian-mgmt-dns-controller",
	"guardian-company-prod",
}

type convergedConfig struct {
	Kubectl                string
	Kubeconfig             string
	KubeAPIServer          string
	RequestTimeout         string
	FluxNamespace          string
	ExpectedRevision       string
	RequiredKustomizations []string
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	kubeAPIServer  string
	requestTimeout string
}

type kubeList struct {
	Items []kubeObject `json:"items"`
}

type kubeObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Status objectStatus `json:"status"`
}

type objectStatus struct {
	LastAppliedRevision string `json:"lastAppliedRevision"`
	// In dark-uplink mode the source is an OCIRepository, whose applied
	// revision is <tag>@<manifest-digest> and does NOT contain the git sha;
	// the pushed git revision surfaces here instead (kustomize-controller
	// records the OCI org.opencontainers.image.revision annotation as the
	// origin revision). Steady GitRepository revisions are main@sha1:<sha>,
	// so LastAppliedRevision alone matches there.
	LastAppliedOriginRevision string      `json:"lastAppliedOriginRevision"`
	Conditions                []condition `json:"conditions"`
}

type condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func main() {
	var cfg convergedConfig
	var requiredKustomizations string
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.KubeAPIServer, "kube-api-server", "", "optional Kubernetes API server override for off-VLAN proof runs")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.FluxNamespace, "flux-namespace", "cozy-fluxcd", "Flux namespace")
	flag.StringVar(&cfg.ExpectedRevision, "expected-revision", "", "optional Git revision that all checked Flux Kustomizations must have applied")
	flag.StringVar(&requiredKustomizations, "required-kustomizations", strings.Join(defaultKustomizations, ","), "comma-separated Flux Kustomizations required for the converged proof")
	flag.Parse()

	cfg.RequiredKustomizations = csv(requiredKustomizations)

	exitIfErr(validateConfig(cfg))
	exitIfErr(runConvergedProof(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg convergedConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if cfg.FluxNamespace == "" {
		return errors.New("--flux-namespace is required")
	}
	if len(cfg.RequiredKustomizations) == 0 {
		return errors.New("--required-kustomizations must not be empty")
	}
	return nil
}

func runConvergedProof(ctx context.Context, cfg convergedConfig) error {
	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		kubeAPIServer:  cfg.KubeAPIServer,
		requestTimeout: cfg.RequestTimeout,
	}

	fmt.Printf("guardian converged proof\n")
	fmt.Printf("fluxNamespace=%s expectedRevision=%s\n", cfg.FluxNamespace, cfg.ExpectedRevision)

	fluxRaw, err := runner.output(ctx, "Flux Kustomizations", "-n", cfg.FluxNamespace, "get", "kustomizations.kustomize.toolkit.fluxcd.io", "-o", "json")
	if err != nil {
		return err
	}
	if err := validateFluxKustomizations(fluxRaw, cfg.RequiredKustomizations, cfg.ExpectedRevision); err != nil {
		return err
	}

	fmt.Printf("converged proof passed\n")
	return nil
}

func validateFluxKustomizations(raw string, required []string, expectedRevision string) error {
	var list kubeList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return fmt.Errorf("parse Flux Kustomizations: %w", err)
	}
	objects := map[string]kubeObject{}
	for _, item := range list.Items {
		objects[item.Metadata.Name] = item
	}
	for _, name := range required {
		item, ok := objects[name]
		if !ok {
			return fmt.Errorf("Flux Kustomization %q is missing", name)
		}
		ready := conditionByType(item.Status.Conditions, "Ready")
		if ready == nil || ready.Status != "True" {
			return fmt.Errorf("Flux Kustomization %q Ready = %s reason=%s message=%s", name, conditionStatus(ready), conditionReason(ready), conditionMessage(ready))
		}
		if expectedRevision != "" &&
			!strings.Contains(item.Status.LastAppliedRevision, expectedRevision) &&
			!strings.Contains(item.Status.LastAppliedOriginRevision, expectedRevision) {
			return fmt.Errorf("Flux Kustomization %q lastAppliedRevision = %q originRevision = %q, want one to contain %q", name, item.Status.LastAppliedRevision, item.Status.LastAppliedOriginRevision, expectedRevision)
		}
	}
	fmt.Printf("Flux Kustomizations ready: %s\n", strings.Join(required, ","))
	return nil
}

func (r kubectlRunner) output(ctx context.Context, label string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.args(args...)...)
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s failed: %w\n%s", label, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (r kubectlRunner) args(args ...string) []string {
	out := make([]string, 0, len(args)+6)
	if r.kubeconfig != "" {
		out = append(out, "--kubeconfig", r.kubeconfig)
	}
	if r.kubeAPIServer != "" {
		out = append(out, "--server", r.kubeAPIServer)
	}
	if r.requestTimeout != "" {
		out = append(out, "--request-timeout", r.requestTimeout)
	}
	out = append(out, args...)
	return out
}

func csv(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func conditionByType(conditions []condition, conditionType string) *condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

func conditionStatus(cond *condition) string {
	if cond == nil {
		return "<missing>"
	}
	return cond.Status
}

func conditionReason(cond *condition) string {
	if cond == nil {
		return "<missing>"
	}
	return cond.Reason
}

func conditionMessage(cond *condition) string {
	if cond == nil {
		return "<missing>"
	}
	return cond.Message
}
