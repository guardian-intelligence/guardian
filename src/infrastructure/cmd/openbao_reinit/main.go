// Command openbao_reinit executes the documented OpenBao reinit (raft-reset
// re-seed of the single static-seal instance) end to end and unattended:
// preconditions, raft reset with dirty-state auto-recovery, raft membership
// drill, custody re-import through the bootstrap importer, scoped re-relay of
// the in-cluster-generated values the importer does not carry, and a
// cluster-wide ExternalSecret force-sync until everything reports synced.
// Runbook: src/infrastructure/runbooks/openbao-static-seal-self-init.md.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	baoapi "github.com/openbao/openbao/api/v2"

	"github.com/guardian-intelligence/guardian/src/infrastructure/cmd/cozystack_openbao_drill/baodrill"
)

const (
	// The pod log line OpenBao emits when it boots against leftover raft
	// state from a partially wiped cluster; the proven recovery is a fresh
	// data PVC for exactly that member.
	dirtyRaftStateMarker = "cluster already has state"

	openBaoPodSelector = "app.kubernetes.io/name=openbao"
	writerServiceAcct  = "secrets-writer"
	kubernetesAuthPath = "kubernetes"
)

type options struct {
	Kubectl        string
	Kubeconfig     string
	KubeAPIServer  string
	RequestTimeout string

	Namespace   string
	StatefulSet string
	Replicas    int
	Service     string
	CASecret    string

	EnvFile  string
	Importer string

	ExpectedRevision string
	LocalRevision    string

	FluxNamespace           string
	SystemKustomization     string
	AppPatchesKustomization string
	TenantHelmRelease       string
	TenantHelmReleaseNS     string

	PodWaitTimeout   time.Duration
	LabelWaitTimeout time.Duration
	SyncTimeout      time.Duration
	PollInterval     time.Duration
}

func main() {
	var opts options
	flag.StringVar(&opts.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&opts.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&opts.KubeAPIServer, "kube-api-server", "", "optional Kubernetes API server override")
	flag.StringVar(&opts.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&opts.Namespace, "namespace", "tenant-guardian", "OpenBao namespace")
	flag.StringVar(&opts.StatefulSet, "statefulset", "guardian-openbao", "OpenBao StatefulSet name")
	flag.IntVar(&opts.Replicas, "replicas", 3, "raft member count to restore after the reset")
	flag.StringVar(&opts.Service, "service", "guardian-openbao-active", "OpenBao service used for port-forward")
	flag.StringVar(&opts.CASecret, "ca-secret", "guardian-openbao-api-tls", "Secret containing the OpenBao API ca.crt")
	flag.StringVar(&opts.EnvFile, "env-file", "", "custody import env file (absolute path; prepared per the runbook, consumed and deleted by the importer)")
	flag.StringVar(&opts.Importer, "importer", "", "path to the openbao_secret_import binary built from this checkout")
	flag.StringVar(&opts.ExpectedRevision, "expected-revision", "", "origin/main HEAD commit the cluster and checkout must both be at")
	flag.StringVar(&opts.LocalRevision, "local-revision", "", "HEAD commit of the checkout that built the importer")
	flag.StringVar(&opts.FluxNamespace, "flux-namespace", "cozy-fluxcd", "Flux namespace")
	flag.StringVar(&opts.SystemKustomization, "system-kustomization", "guardian-system", "Flux Kustomization carrying the OpenBao self-init block")
	flag.StringVar(&opts.AppPatchesKustomization, "app-patches-kustomization", "guardian-mgmt-app-patches", "Flux Kustomization carrying the tenant namespace Pod Security postRenderer")
	flag.StringVar(&opts.TenantHelmRelease, "tenant-helmrelease", "tenant-guardian", "tenant HelmRelease that renders the OpenBao namespace")
	flag.StringVar(&opts.TenantHelmReleaseNS, "tenant-helmrelease-namespace", "tenant-root", "namespace of the tenant HelmRelease")
	flag.DurationVar(&opts.PodWaitTimeout, "pod-wait-timeout", 10*time.Minute, "timeout for the raft members to reach Running/Ready after the reset")
	flag.DurationVar(&opts.LabelWaitTimeout, "label-wait-timeout", 5*time.Minute, "timeout for the Pod Security label to reappear after remediation")
	flag.DurationVar(&opts.SyncTimeout, "sync-timeout", 10*time.Minute, "timeout for every ExternalSecret to report synced after the force-sync")
	flag.DurationVar(&opts.PollInterval, "poll-interval", 10*time.Second, "poll interval for pod, label, and ExternalSecret waits")
	flag.Parse()

	exitIfErr(run(context.Background(), opts))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func run(ctx context.Context, opts options) error {
	if err := validateOptions(opts); err != nil {
		return err
	}
	runner := kubectlRunner{
		bin:            opts.Kubectl,
		kubeconfig:     opts.Kubeconfig,
		kubeAPIServer:  opts.KubeAPIServer,
		requestTimeout: opts.RequestTimeout,
	}

	fmt.Printf("guardian openbao reinit\nnamespace=%s statefulset=%s expected-revision=%s\n", opts.Namespace, opts.StatefulSet, opts.ExpectedRevision)

	if err := checkPreconditions(ctx, runner, opts); err != nil {
		return err
	}
	if err := resetRaft(ctx, runner, opts); err != nil {
		return err
	}
	if err := baodrill.Run(ctx, baodrill.Config{
		Kubectl:        opts.Kubectl,
		Kubeconfig:     opts.Kubeconfig,
		KubeAPIServer:  opts.KubeAPIServer,
		RequestTimeout: opts.RequestTimeout,
		Namespace:      opts.Namespace,
		StatefulSet:    opts.StatefulSet,
	}); err != nil {
		return err
	}
	if err := runImporter(ctx, opts); err != nil {
		return err
	}
	if err := relayGeneratedValues(ctx, runner, opts); err != nil {
		return err
	}
	if err := forceSyncExternalSecrets(ctx, runner, opts); err != nil {
		return err
	}
	fmt.Printf("\nOpenBao reinit complete: raft reset, %d members verified, custody re-imported, %d generated values re-relayed, every ExternalSecret synced\n", opts.Replicas, len(relayPlan()))
	return nil
}

func validateOptions(opts options) error {
	if opts.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if opts.Importer == "" {
		return errors.New("--importer is required (path to the openbao_secret_import binary)")
	}
	if opts.EnvFile == "" {
		return errors.New("--env-file is required (prepare it per the runbook: custody restore, then build import.env)")
	}
	if !filepath.IsAbs(opts.EnvFile) {
		return fmt.Errorf("--env-file %q must be absolute: the tool runs from a Bazel runfiles directory, so a relative path would not resolve to the custody working copy", opts.EnvFile)
	}
	if opts.ExpectedRevision == "" || opts.LocalRevision == "" {
		return errors.New("--expected-revision and --local-revision are required")
	}
	if opts.Replicas <= 0 {
		return fmt.Errorf("--replicas %d must be positive", opts.Replicas)
	}
	return nil
}

// --- preconditions -----------------------------------------------------------

func checkPreconditions(ctx context.Context, runner kubectlRunner, opts options) error {
	fmt.Printf("\n## precondition: checkout matches origin/main\n")
	if err := localRevisionProblem(opts.LocalRevision, opts.ExpectedRevision); err != nil {
		return err
	}
	fmt.Printf("checkout at %s\n", opts.LocalRevision)

	fmt.Printf("\n## precondition: %s Kustomization Ready at origin/main\n", opts.SystemKustomization)
	raw, err := runner.output(ctx, "read "+opts.SystemKustomization+" Kustomization", "-n", opts.FluxNamespace, "get", "kustomization.kustomize.toolkit.fluxcd.io/"+opts.SystemKustomization, "-o", "json")
	if err != nil {
		return err
	}
	if problems := kustomizationProblems(raw, opts.ExpectedRevision); len(problems) > 0 {
		return fmt.Errorf("%s is not reconciled at origin/main (%s): %s — the self-init block the raft reset boots into would be stale; wait for Flux (tools/ops/cluster-watch --status) before retrying", opts.SystemKustomization, opts.ExpectedRevision, strings.Join(problems, "; "))
	}
	fmt.Printf("%s Ready at %s\n", opts.SystemKustomization, opts.ExpectedRevision)

	fmt.Printf("\n## precondition: %s namespace enforces privileged Pod Security\n", opts.Namespace)
	if err := ensurePrivilegedPSALabel(ctx, runner, opts); err != nil {
		return err
	}

	fmt.Printf("\n## precondition: custody import env file\n")
	info, err := os.Stat(opts.EnvFile)
	if err != nil {
		return fmt.Errorf("custody import env file %s: %w — prepare it first (aspect infra custody --action restore, then build import.env per the runbook's Bootstrap Secret Import section)", opts.EnvFile, err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("custody import env file %s is empty", opts.EnvFile)
	}
	fmt.Printf("%s present (%d bytes)\n", opts.EnvFile, info.Size())
	return nil
}

func localRevisionProblem(local, expected string) error {
	if local != expected {
		return fmt.Errorf("checkout HEAD %s is not origin/main %s: the importer binary embeds the import plan, and seeding from a stale plan is the reinit's #1 historical footgun — run from an up-to-date origin/main checkout", local, expected)
	}
	return nil
}

// ensurePrivilegedPSALabel verifies pod-security.kubernetes.io/enforce=privileged
// on the OpenBao namespace. A Cozystack tenant-chart regeneration can SSA-stomp
// the postRenderer that sets it (base/app-patches/tenant-guardian-namespace-pod-security.yaml);
// without the label the StatefulSet cannot recreate pods (the hostPath seal
// volume violates baseline PSA) and a partial boot leaves dirty raft state. The
// remediation is the same nudge an operator would do: reconcile the app-patches
// Kustomization, then the tenant HelmRelease, then re-check.
func ensurePrivilegedPSALabel(ctx context.Context, runner kubectlRunner, opts options) error {
	ok, err := namespaceEnforcesPrivileged(ctx, runner, opts.Namespace)
	if err != nil {
		return err
	}
	if ok {
		fmt.Printf("namespace %s enforces privileged\n", opts.Namespace)
		return nil
	}

	fmt.Printf("namespace %s is missing pod-security.kubernetes.io/enforce=privileged (tenant-chart SSA stomp); remediating\n", opts.Namespace)
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := runner.run(ctx, "request "+opts.AppPatchesKustomization+" reconciliation", "-n", opts.FluxNamespace, "annotate", "kustomization.kustomize.toolkit.fluxcd.io/"+opts.AppPatchesKustomization, "reconcile.fluxcd.io/requestedAt="+stamp, "--overwrite"); err != nil {
		return err
	}
	if err := waitReconcileHandled(ctx, runner, opts, "kustomization.kustomize.toolkit.fluxcd.io/"+opts.AppPatchesKustomization, opts.FluxNamespace, stamp); err != nil {
		return err
	}
	if err := runner.run(ctx, "request "+opts.TenantHelmRelease+" HelmRelease reconciliation", "-n", opts.TenantHelmReleaseNS, "annotate", "helmrelease.helm.toolkit.fluxcd.io/"+opts.TenantHelmRelease, "reconcile.fluxcd.io/requestedAt="+stamp, "--overwrite"); err != nil {
		return err
	}

	deadline := time.Now().Add(opts.LabelWaitTimeout)
	for {
		ok, err := namespaceEnforcesPrivileged(ctx, runner, opts.Namespace)
		if err != nil {
			return err
		}
		if ok {
			fmt.Printf("namespace %s enforces privileged after remediation\n", opts.Namespace)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("namespace %s still lacks pod-security.kubernetes.io/enforce=privileged after %s: the postRenderer in base/app-patches/tenant-guardian-namespace-pod-security.yaml did not land — do not reset raft in this state (pods cannot recreate and a partial boot leaves dirty raft state)", opts.Namespace, opts.LabelWaitTimeout)
		}
		time.Sleep(opts.PollInterval)
	}
}

func namespaceEnforcesPrivileged(ctx context.Context, runner kubectlRunner, namespace string) (bool, error) {
	raw, err := runner.output(ctx, "read namespace "+namespace, "get", "namespace/"+namespace, "-o", "json")
	if err != nil {
		return false, err
	}
	return namespaceHasPrivilegedEnforce(raw)
}

func namespaceHasPrivilegedEnforce(raw string) (bool, error) {
	var ns struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &ns); err != nil {
		return false, fmt.Errorf("parse namespace JSON: %w", err)
	}
	return ns.Metadata.Labels["pod-security.kubernetes.io/enforce"] == "privileged", nil
}

func kustomizationProblems(raw, expectedRevision string) []string {
	var obj struct {
		Status struct {
			LastAppliedRevision string          `json:"lastAppliedRevision"`
			Conditions          []statusCondish `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return []string{"parse Kustomization JSON: " + err.Error()}
	}
	var problems []string
	if !hasReadyCondition(obj.Status.Conditions) {
		problems = append(problems, "Ready condition is not True ("+readyMessage(obj.Status.Conditions)+")")
	}
	if expectedRevision == "" {
		problems = append(problems, "expected revision is empty")
	} else if !strings.Contains(obj.Status.LastAppliedRevision, expectedRevision) {
		problems = append(problems, fmt.Sprintf("lastAppliedRevision %q does not carry %s", obj.Status.LastAppliedRevision, expectedRevision))
	}
	return problems
}

type statusCondish struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func hasReadyCondition(conditions []statusCondish) bool {
	for _, c := range conditions {
		if c.Type == "Ready" && c.Status == "True" {
			return true
		}
	}
	return false
}

func readyMessage(conditions []statusCondish) string {
	for _, c := range conditions {
		if c.Type == "Ready" {
			return c.Reason + ": " + c.Message
		}
	}
	return "no Ready condition"
}

func waitReconcileHandled(ctx context.Context, runner kubectlRunner, opts options, resource, namespace, stamp string) error {
	deadline := time.Now().Add(opts.LabelWaitTimeout)
	for {
		raw, err := runner.output(ctx, "read "+resource+" reconcile status", "-n", namespace, "get", resource, "-o", "json")
		if err != nil {
			return err
		}
		handled, err := reconcileHandled(raw, stamp)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not handle the reconcile request within %s", resource, opts.LabelWaitTimeout)
		}
		time.Sleep(opts.PollInterval)
	}
}

func reconcileHandled(raw, stamp string) (bool, error) {
	var obj struct {
		Status struct {
			LastHandledReconcileAt string          `json:"lastHandledReconcileAt"`
			Conditions             []statusCondish `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return false, fmt.Errorf("parse reconcile status JSON: %w", err)
	}
	return obj.Status.LastHandledReconcileAt == stamp && hasReadyCondition(obj.Status.Conditions), nil
}

// --- raft reset --------------------------------------------------------------

func resetRaft(ctx context.Context, runner kubectlRunner, opts options) error {
	sts := "statefulset.apps/" + opts.StatefulSet
	if err := runner.run(ctx, "scale OpenBao to 0", "-n", opts.Namespace, "scale", sts, "--replicas=0"); err != nil {
		return err
	}
	if err := runner.run(ctx, "wait for OpenBao pods to terminate", "-n", opts.Namespace, "wait", "pod", "-l", openBaoPodSelector, "--for=delete", "--timeout=5m"); err != nil {
		return err
	}
	pvcs := dataPVCNames(opts.StatefulSet, opts.Replicas)
	deleteArgs := append([]string{"-n", opts.Namespace, "delete", "pvc"}, pvcs...)
	deleteArgs = append(deleteArgs, "--ignore-not-found")
	if err := runner.run(ctx, "delete raft data PVCs (audit PVCs stay)", deleteArgs...); err != nil {
		return err
	}
	if err := runner.run(ctx, fmt.Sprintf("scale OpenBao to %d", opts.Replicas), "-n", opts.Namespace, "scale", sts, fmt.Sprintf("--replicas=%d", opts.Replicas)); err != nil {
		return err
	}
	return waitPodsReady(ctx, runner, opts)
}

func dataPVCNames(statefulSet string, replicas int) []string {
	out := make([]string, 0, replicas)
	for i := 0; i < replicas; i++ {
		out = append(out, fmt.Sprintf("data-%s-%d", statefulSet, i))
	}
	return out
}

// waitPodsReady polls until every raft member is Running with all containers
// ready. A member that crashloops on leftover raft state ("cluster already has
// state") is auto-recovered exactly once by deleting its pod and data PVC so
// the StatefulSet re-creates both fresh; any other crashloop, or a second one
// on the same member, fails the run.
func waitPodsReady(ctx context.Context, runner kubectlRunner, opts options) error {
	recovered := map[string]bool{}
	deadline := time.Now().Add(opts.PodWaitTimeout)
	for {
		raw, err := runner.output(ctx, "read OpenBao pods", "-n", opts.Namespace, "get", "pods", "-l", openBaoPodSelector, "-o", "json")
		if err != nil {
			return err
		}
		state, err := classifyPods(raw, opts.Replicas)
		if err != nil {
			return err
		}
		if state.AllReady {
			fmt.Printf("all %d OpenBao pods Running and ready\n", opts.Replicas)
			return nil
		}
		for _, pod := range state.CrashLooping {
			logs, logErr := runner.output(ctx, "read crashlooping pod logs", "-n", opts.Namespace, "logs", "pod/"+pod, "-c", "openbao", "--tail=200")
			if logErr != nil {
				logs = ""
			}
			if !dirtyRaftState(logs) {
				fmt.Printf("pod %s is crashlooping without the dirty-raft-state marker; waiting\n", pod)
				continue
			}
			if recovered[pod] {
				return fmt.Errorf("pod %s crashlooped on %q again after a fresh-PVC recovery; manual investigation required", pod, dirtyRaftStateMarker)
			}
			recovered[pod] = true
			fmt.Printf("pod %s booted against dirty raft state; recovering with a fresh data PVC\n", pod)
			if err := runner.run(ctx, "delete dirty data PVC for "+pod, "-n", opts.Namespace, "delete", "pvc", "data-"+pod, "--wait=false", "--ignore-not-found"); err != nil {
				return err
			}
			if err := runner.run(ctx, "delete crashlooping pod "+pod, "-n", opts.Namespace, "delete", "pod", pod, "--ignore-not-found"); err != nil {
				return err
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("OpenBao pods did not become ready within %s: %s", opts.PodWaitTimeout, state.Summary)
		}
		fmt.Printf("waiting for OpenBao pods: %s\n", state.Summary)
		time.Sleep(opts.PollInterval)
	}
}

type podSetState struct {
	AllReady     bool
	CrashLooping []string
	Summary      string
}

func classifyPods(raw string, expected int) (podSetState, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Name  string `json:"name"`
					Ready bool   `json:"ready"`
					State struct {
						Waiting struct {
							Reason string `json:"reason"`
						} `json:"waiting"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return podSetState{}, fmt.Errorf("parse pod list JSON: %w", err)
	}
	state := podSetState{AllReady: len(list.Items) == expected}
	var summary []string
	for _, pod := range list.Items {
		ready := pod.Status.Phase == "Running" && len(pod.Status.ContainerStatuses) > 0
		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready {
				ready = false
			}
			if cs.State.Waiting.Reason == "CrashLoopBackOff" {
				state.CrashLooping = append(state.CrashLooping, pod.Metadata.Name)
			}
		}
		if !ready {
			state.AllReady = false
			summary = append(summary, fmt.Sprintf("%s phase=%s", pod.Metadata.Name, pod.Status.Phase))
		}
	}
	if len(list.Items) != expected {
		summary = append(summary, fmt.Sprintf("%d/%d pods exist", len(list.Items), expected))
	}
	state.Summary = strings.Join(summary, ", ")
	sort.Strings(state.CrashLooping)
	return state, nil
}

func dirtyRaftState(logs string) bool {
	return strings.Contains(logs, dirtyRaftStateMarker)
}

// --- custody import ----------------------------------------------------------

func runImporter(ctx context.Context, opts options) error {
	port, err := randomFreePort()
	if err != nil {
		return err
	}
	args := importerArgs(opts, port)
	fmt.Printf("\n## custody re-import via %s\n", opts.Importer)
	cmd := exec.CommandContext(ctx, opts.Importer, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("custody importer: %w", err)
	}
	return nil
}

func importerArgs(opts options, localPort int) []string {
	args := []string{
		"--kubectl", opts.Kubectl,
		"--env-file", opts.EnvFile,
		"--delete-env-file",
		"--local-port", fmt.Sprintf("%d", localPort),
		"--request-timeout", opts.RequestTimeout,
		"--namespace", opts.Namespace,
		"--service", opts.Service,
		"--ca-secret", opts.CASecret,
	}
	if opts.Kubeconfig != "" {
		args = append(args, "--kubeconfig", opts.Kubeconfig)
	}
	if opts.KubeAPIServer != "" {
		args = append(args, "--kube-api-server", opts.KubeAPIServer)
	}
	return args
}

// randomFreePort asks the kernel for an ephemeral port. Fixed local ports
// collide with parallel agent sessions sharing the operator box.
func randomFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find free local port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release probed local port: %w", err)
	}
	return port, nil
}

// --- re-relay of in-cluster-generated values ---------------------------------

// relayTarget re-seeds one kv path the importer does not carry, sourced from a
// still-materialized Kubernetes Secret (ExternalSecrets are Orphan/Retain, so
// the projected Secrets survive the raft wipe) and written through that
// consumer namespace's own guardian-writer role.
type relayTarget struct {
	Name string
	// ConsumerNamespace holds the secrets-writer ServiceAccount and names the
	// guardian-writer-<ns> role; the write is confined to its kv subtree.
	ConsumerNamespace string
	APIPath           string
	SourceNamespace   string
	SourceSecret      string
	// Keys maps kv property name -> source Secret data key.
	Keys map[string]string
	// MissingHint names the upstream source of truth to consult when the
	// materialized Secret is gone.
	MissingHint string
}

func relayPlan() []relayTarget {
	return []relayTarget{
		{
			Name:              "analytics ClickHouse ingest password",
			ConsumerNamespace: "guardian-analytics",
			APIPath:           "kv/data/guardian/guardian-mgmt/guardian-analytics/clickhouse",
			SourceNamespace:   "guardian-analytics",
			SourceSecret:      "analytics-ch-ingest",
			Keys:              map[string]string{"ingest": "ingest"},
			MissingHint:       "the chart-generated Secret clickhouse-analytics-credentials key ingest in tenant-root (runbooks/analytics-clickhouse.md)",
		},
		{
			Name:              "postflight control-plane Postgres URI",
			ConsumerNamespace: "postflight-runner",
			APIPath:           "kv/data/guardian/guardian-mgmt/postflight-runner/postgres",
			SourceNamespace:   "tenant-root",
			SourceSecret:      "postgres-postflight-controlplane-app",
			Keys:              map[string]string{"uri": "uri"},
			MissingHint:       "the CNPG-generated app Secret of the postflight-controlplane Postgres app in tenant-root (base/apps/postflight-controlplane-postgres.yaml)",
		},
		{
			Name:              "external-dns Cloudflare token",
			ConsumerNamespace: "external-dns",
			APIPath:           "kv/data/guardian/guardian-mgmt/external-dns/cloudflare",
			SourceNamespace:   "external-dns",
			SourceSecret:      "cloudflare-external-dns",
			Keys:              map[string]string{"CF_API_TOKEN": "CF_API_TOKEN"},
			MissingHint:       "tofu -chdir=src/infrastructure/bootstrap/guardian-mgmt-cloudflare-tokens output -raw external_dns_token_value",
		},
		{
			Name:              "backups R2 keypair",
			ConsumerNamespace: "tenant-root",
			APIPath:           "kv/data/guardian/guardian-mgmt/tenant-root/backups-r2",
			SourceNamespace:   "tenant-root",
			SourceSecret:      "guardian-backups-creds",
			Keys: map[string]string{
				"accessKey":  "accessKey",
				"secretKey":  "secretKey",
				"endpoint":   "endpoint",
				"bucketName": "bucketName",
				"region":     "region",
			},
			MissingHint: "tofu -chdir=src/infrastructure/bootstrap/guardian-mgmt-cloudflare-tokens outputs r2_backups_access_key_id / r2_backups_secret_access_key (bucketName=guardian-backups, region=auto)",
		},
	}
}

func relayGeneratedValues(ctx context.Context, runner kubectlRunner, opts options) error {
	fmt.Printf("\n## re-relay of in-cluster-generated values\n")
	caPEM, err := openBaoCA(ctx, runner, opts)
	if err != nil {
		return err
	}
	port, err := randomFreePort()
	if err != nil {
		return err
	}
	forward, err := startPortForward(ctx, runner, opts, port)
	if err != nil {
		return err
	}
	defer forward.stop()

	for _, target := range relayPlan() {
		raw, err := runner.output(ctx, "read source Secret "+target.SourceNamespace+"/"+target.SourceSecret, "-n", target.SourceNamespace, "get", "secret/"+target.SourceSecret, "-o", "json")
		if err != nil {
			return fmt.Errorf("source Secret %s/%s for %s is unavailable: %w — re-relay this value by hand from %s", target.SourceNamespace, target.SourceSecret, target.Name, err, target.MissingHint)
		}
		data, err := decodeSecretData(raw)
		if err != nil {
			return fmt.Errorf("decode source Secret %s/%s: %w", target.SourceNamespace, target.SourceSecret, err)
		}
		values, err := relayValues(target, data)
		if err != nil {
			return err
		}
		jwt, err := writerToken(ctx, runner, target.ConsumerNamespace)
		if err != nil {
			return err
		}
		client, err := authenticatedOpenBaoClient(ctx, opts, port, caPEM, "guardian-writer-"+target.ConsumerNamespace, jwt)
		if err != nil {
			return fmt.Errorf("login as guardian-writer-%s for %s: %w", target.ConsumerNamespace, target.Name, err)
		}
		if err := writeAndVerify(ctx, client, target.APIPath, values); err != nil {
			return fmt.Errorf("re-relay %s: %w", target.Name, err)
		}
		fmt.Printf("re-relayed %s to %s properties: %s\n", target.Name, target.APIPath, strings.Join(sortedKeys(values), ","))
	}
	return nil
}

// relayValues resolves the target's kv properties from decoded Secret data
// with `test -s` semantics: a missing or empty value fails the run rather than
// overwriting a good secret with nothing.
func relayValues(target relayTarget, data map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(target.Keys))
	for property, key := range target.Keys {
		value := data[key]
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("source Secret %s/%s key %s for %s is missing or empty; refusing to write an empty value — source it from %s", target.SourceNamespace, target.SourceSecret, key, target.Name, target.MissingHint)
		}
		out[property] = value
	}
	return out, nil
}

func decodeSecretData(raw string) (map[string]string, error) {
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return nil, fmt.Errorf("parse Secret JSON: %w", err)
	}
	out := make(map[string]string, len(secret.Data))
	for key, encoded := range secret.Data {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode Secret key %s: %w", key, err)
		}
		out[key] = string(decoded)
	}
	return out, nil
}

func openBaoCA(ctx context.Context, runner kubectlRunner, opts options) ([]byte, error) {
	raw, err := runner.output(ctx, "read OpenBao CA Secret", "-n", opts.Namespace, "get", "secret/"+opts.CASecret, "-o", "json")
	if err != nil {
		return nil, err
	}
	data, err := decodeSecretData(raw)
	if err != nil {
		return nil, fmt.Errorf("decode CA Secret %s: %w", opts.CASecret, err)
	}
	caPEM := data["ca.crt"]
	if caPEM == "" {
		return nil, fmt.Errorf("Secret %s has no ca.crt", opts.CASecret)
	}
	return []byte(caPEM), nil
}

func writerToken(ctx context.Context, runner kubectlRunner, namespace string) (string, error) {
	out, err := runner.output(ctx, "mint "+writerServiceAcct+" token in "+namespace, "-n", namespace, "create", "token", writerServiceAcct, "--audience=openbao", "--duration=10m")
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(out)
	if token == "" {
		return "", fmt.Errorf("kubectl create token %s -n %s returned an empty token", writerServiceAcct, namespace)
	}
	return token, nil
}

func authenticatedOpenBaoClient(ctx context.Context, opts options, localPort int, caPEM []byte, authRole, jwt string) (*baoapi.Client, error) {
	cfg := baoapi.DefaultConfig()
	cfg.Address = fmt.Sprintf("https://127.0.0.1:%d", localPort)
	if err := cfg.ConfigureTLS(&baoapi.TLSConfig{
		CACertBytes:   caPEM,
		TLSServerName: fmt.Sprintf("%s.%s.svc", opts.Service, opts.Namespace),
	}); err != nil {
		return nil, fmt.Errorf("configure OpenBao TLS: %w", err)
	}
	client, err := baoapi.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	secret, err := client.Logical().WriteWithContext(ctx, "auth/"+kubernetesAuthPath+"/login", map[string]interface{}{
		"role": authRole,
		"jwt":  jwt,
	})
	if err != nil {
		return nil, fmt.Errorf("login to OpenBao role %s: %w", authRole, err)
	}
	if secret == nil || secret.Auth == nil || secret.Auth.ClientToken == "" {
		return nil, fmt.Errorf("login to OpenBao role %s returned no client token", authRole)
	}
	client.SetToken(secret.Auth.ClientToken)
	return client, nil
}

func writeAndVerify(ctx context.Context, client *baoapi.Client, apiPath string, values map[string]string) error {
	body := map[string]interface{}{"data": stringMapToInterface(values)}
	if _, err := client.Logical().WriteWithContext(ctx, apiPath, body); err != nil {
		return fmt.Errorf("write %s: %w", apiPath, err)
	}
	secret, err := client.Logical().ReadWithContext(ctx, apiPath)
	if err != nil {
		return fmt.Errorf("verify %s: %w", apiPath, err)
	}
	if secret == nil || secret.Data == nil {
		return fmt.Errorf("verify %s: empty readback", apiPath)
	}
	data, ok := secret.Data["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("verify %s: readback missing kv-v2 data", apiPath)
	}
	for key, want := range values {
		if got, ok := data[key].(string); !ok || got != want {
			return fmt.Errorf("verify %s: property %s mismatch", apiPath, key)
		}
	}
	return nil
}

func stringMapToInterface(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func sortedKeys(in map[string]string) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// --- ExternalSecret convergence ----------------------------------------------

// forceSyncExternalSecrets nudges every secret store and ExternalSecret, then
// polls until all of them report Ready. The store nudge is load-bearing: a
// (Cluster)SecretStore that validated while OpenBao was down wedges on a stale
// "InvalidProviderConfig/unable to create client" condition until poked.
func forceSyncExternalSecrets(ctx context.Context, runner kubectlRunner, opts options) error {
	fmt.Printf("\n## force-sync secret stores and ExternalSecrets\n")
	stamp := fmt.Sprintf("%d", time.Now().Unix())
	if err := runner.run(ctx, "nudge every ClusterSecretStore", "annotate", "clustersecretstore", "--all", "force-sync="+stamp, "--overwrite"); err != nil {
		return err
	}
	// Namespaced SecretStores wedge the same way; tolerate none existing.
	if err := runner.run(ctx, "nudge every SecretStore", "annotate", "secretstore", "--all", "--all-namespaces", "force-sync="+stamp, "--overwrite"); err != nil {
		fmt.Printf("WARN: SecretStore nudge failed (fine when none exist): %v\n", err)
	}
	if err := runner.run(ctx, "force-sync every ExternalSecret", "annotate", "externalsecret", "--all", "--all-namespaces", "force-sync="+stamp, "--overwrite"); err != nil {
		return err
	}

	deadline := time.Now().Add(opts.SyncTimeout)
	for {
		storesRaw, err := runner.output(ctx, "read ClusterSecretStores", "get", "clustersecretstore", "-o", "json")
		if err != nil {
			return err
		}
		storeStragglers, err := notReadyItems(storesRaw)
		if err != nil {
			return fmt.Errorf("parse ClusterSecretStore list: %w", err)
		}
		esRaw, err := runner.output(ctx, "read ExternalSecrets", "get", "externalsecret", "--all-namespaces", "-o", "json")
		if err != nil {
			return err
		}
		esStragglers, err := notReadyItems(esRaw)
		if err != nil {
			return fmt.Errorf("parse ExternalSecret list: %w", err)
		}
		if len(storeStragglers) == 0 && len(esStragglers) == 0 {
			fmt.Printf("every ClusterSecretStore and ExternalSecret reports Ready\n")
			return nil
		}
		if time.Now().After(deadline) {
			stragglers := append(storeStragglers, esStragglers...)
			return fmt.Errorf("secret sync did not converge within %s; stragglers:\n%s", opts.SyncTimeout, strings.Join(stragglers, "\n"))
		}
		fmt.Printf("waiting on %d store(s) and %d ExternalSecret(s)\n", len(storeStragglers), len(esStragglers))
		time.Sleep(opts.PollInterval)
	}
}

func notReadyItems(raw string) ([]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Namespace string `json:"namespace"`
				Name      string `json:"name"`
			} `json:"metadata"`
			Status struct {
				Conditions []statusCondish `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, err
	}
	var out []string
	for _, item := range list.Items {
		if hasReadyCondition(item.Status.Conditions) {
			continue
		}
		name := item.Metadata.Name
		if item.Metadata.Namespace != "" {
			name = item.Metadata.Namespace + "/" + name
		}
		out = append(out, name+": "+readyMessage(item.Status.Conditions))
	}
	sort.Strings(out)
	return out, nil
}

// --- kubectl -----------------------------------------------------------------

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	kubeAPIServer  string
	requestTimeout string
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
		out = append(out, "--request-timeout="+r.requestTimeout)
	}
	out = append(out, args...)
	return out
}

func (r kubectlRunner) run(ctx context.Context, label string, args ...string) error {
	fmt.Printf("\n## %s\n", label)
	cmd := exec.CommandContext(ctx, r.bin, r.args(args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	fmt.Print(buf.String())
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func (r kubectlRunner) output(ctx context.Context, label string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.args(args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("%s: %w\n%s", label, err, buf.String())
	}
	return buf.String(), nil
}

type portForward struct {
	cmd    *exec.Cmd
	done   chan error
	output *bytes.Buffer
}

func startPortForward(ctx context.Context, runner kubectlRunner, opts options, localPort int) (*portForward, error) {
	var output bytes.Buffer
	args := runner.args("-n", opts.Namespace, "port-forward", "svc/"+opts.Service, fmt.Sprintf("%d:8200", localPort))
	cmd := exec.CommandContext(ctx, runner.bin, args...)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start OpenBao port-forward: %w", err)
	}
	forward := &portForward{
		cmd:    cmd,
		done:   make(chan error, 1),
		output: &output,
	}
	go func() {
		forward.done <- cmd.Wait()
	}()
	if err := forward.wait(localPort); err != nil {
		forward.stop()
		return nil, err
	}
	return forward, nil
}

func (p *portForward) wait(localPort int) error {
	deadline := time.Now().Add(15 * time.Second)
	address := fmt.Sprintf("127.0.0.1:%d", localPort)
	for time.Now().Before(deadline) {
		select {
		case err := <-p.done:
			return fmt.Errorf("OpenBao port-forward exited before it was ready: %w\n%s", err, p.output.String())
		default:
		}
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("OpenBao port-forward did not become ready on %s\n%s", address, p.output.String())
}

func (p *portForward) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}
