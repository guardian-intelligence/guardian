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
	"guardian-mgmt-app-patches",
	"guardian-system",
	"guardian-openbao-ops",
	"guardian-mgmt-dns-controller",
	"guardian-company-prod",
	"guardian-openbao-ops-crds",
	"guardian-openbao-ops-controller",
	"guardian-openbao-ops-state",
}

var defaultOpenBaoObjects = []string{
	"OpenBaoAuthBackend/kubernetes",
	"OpenBaoKubernetesAuthRole/external-dns",
	"OpenBaoKubernetesAuthRole/ops-controller",
	"OpenBaoMount/kv",
	"OpenBaoMount/transit",
	"OpenBaoMountTune/kv",
	"OpenBaoPolicy/external-dns",
	"OpenBaoPolicy/ops-controller",
}

var defaultCertificates = []string{
	"guardian-openbao-listener-ca",
	"guardian-openbao-api",
}

type cutoverConfig struct {
	Kubectl                string
	Kubeconfig             string
	KubeAPIServer          string
	RequestTimeout         string
	FluxNamespace          string
	CertManagerNamespace   string
	CertManagerDeployment  string
	OpenBaoNamespace       string
	OpenBaoHelmRelease     string
	OpenBaoStatefulSet     string
	ControllerDeployment   string
	ExpectedRevision       string
	RequiredKustomizations []string
	RequiredOpenBaoObjects []string
	RequiredCertificates   []string
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
	Spec struct {
		Replicas       int `json:"replicas"`
		UpdateStrategy struct {
			Type string `json:"type"`
		} `json:"updateStrategy"`
		Template struct {
			Spec struct {
				Containers []containerSpec `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status objectStatus `json:"status"`
}

type containerSpec struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type objectStatus struct {
	LastAppliedRevision string      `json:"lastAppliedRevision"`
	ReadyReplicas       int         `json:"readyReplicas"`
	Replicas            int         `json:"replicas"`
	UpdatedReplicas     int         `json:"updatedReplicas"`
	CurrentRevision     string      `json:"currentRevision"`
	UpdateRevision      string      `json:"updateRevision"`
	LastError           string      `json:"lastError"`
	Conditions          []condition `json:"conditions"`
}

type condition struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type openBaoStatus struct {
	Initialized bool   `json:"initialized"`
	Sealed      bool   `json:"sealed"`
	HAEnabled   bool   `json:"ha_enabled"`
	ClusterID   string `json:"cluster_id"`
	Version     string `json:"version"`
}

func main() {
	var cfg cutoverConfig
	var requiredKustomizations string
	var requiredOpenBaoObjects string
	var requiredCertificates string
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.KubeAPIServer, "kube-api-server", "", "optional Kubernetes API server override for off-VLAN proof runs")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.FluxNamespace, "flux-namespace", "cozy-fluxcd", "Flux namespace")
	flag.StringVar(&cfg.CertManagerNamespace, "cert-manager-namespace", "cozy-cert-manager", "cert-manager namespace")
	flag.StringVar(&cfg.CertManagerDeployment, "cert-manager-deployment", "cert-manager", "cert-manager controller Deployment name")
	flag.StringVar(&cfg.OpenBaoNamespace, "openbao-namespace", "tenant-guardian", "OpenBao namespace")
	flag.StringVar(&cfg.OpenBaoHelmRelease, "openbao-helmrelease", "guardian-openbao", "OpenBao HelmRelease name")
	flag.StringVar(&cfg.OpenBaoStatefulSet, "openbao-statefulset", "guardian-openbao", "OpenBao StatefulSet name")
	flag.StringVar(&cfg.ControllerDeployment, "controller-deployment", "openbao-ops-controller", "OpenBao ops-controller Deployment name")
	flag.StringVar(&cfg.ExpectedRevision, "expected-revision", "", "optional Git revision that all checked Flux Kustomizations must have applied")
	flag.StringVar(&requiredKustomizations, "required-kustomizations", strings.Join(defaultKustomizations, ","), "comma-separated Flux Kustomizations required for cutover proof")
	flag.StringVar(&requiredOpenBaoObjects, "required-openbao-objects", strings.Join(defaultOpenBaoObjects, ","), "comma-separated Kind/name OpenBao CRs required for cutover proof")
	flag.StringVar(&requiredCertificates, "required-certificates", strings.Join(defaultCertificates, ","), "comma-separated cert-manager Certificates required for cutover proof")
	flag.Parse()

	cfg.RequiredKustomizations = csv(requiredKustomizations)
	cfg.RequiredOpenBaoObjects = csv(requiredOpenBaoObjects)
	cfg.RequiredCertificates = csv(requiredCertificates)

	exitIfErr(validateConfig(cfg))
	exitIfErr(runCutoverProof(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg cutoverConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if cfg.FluxNamespace == "" {
		return errors.New("--flux-namespace is required")
	}
	if cfg.CertManagerNamespace == "" {
		return errors.New("--cert-manager-namespace is required")
	}
	if cfg.CertManagerDeployment == "" {
		return errors.New("--cert-manager-deployment is required")
	}
	if cfg.OpenBaoNamespace == "" {
		return errors.New("--openbao-namespace is required")
	}
	if cfg.OpenBaoHelmRelease == "" {
		return errors.New("--openbao-helmrelease is required")
	}
	if cfg.OpenBaoStatefulSet == "" {
		return errors.New("--openbao-statefulset is required")
	}
	if cfg.ControllerDeployment == "" {
		return errors.New("--controller-deployment is required")
	}
	if len(cfg.RequiredKustomizations) == 0 {
		return errors.New("--required-kustomizations must not be empty")
	}
	if len(cfg.RequiredOpenBaoObjects) == 0 {
		return errors.New("--required-openbao-objects must not be empty")
	}
	if len(cfg.RequiredCertificates) == 0 {
		return errors.New("--required-certificates must not be empty")
	}
	return nil
}

func runCutoverProof(ctx context.Context, cfg cutoverConfig) error {
	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		kubeAPIServer:  cfg.KubeAPIServer,
		requestTimeout: cfg.RequestTimeout,
	}

	fmt.Printf("guardian openbao cutover proof\n")
	fmt.Printf("fluxNamespace=%s openbaoNamespace=%s openbaoHelmRelease=%s openbaoStatefulSet=%s controllerDeployment=%s\n", cfg.FluxNamespace, cfg.OpenBaoNamespace, cfg.OpenBaoHelmRelease, cfg.OpenBaoStatefulSet, cfg.ControllerDeployment)

	fluxRaw, err := runner.output(ctx, "Flux Kustomizations", "-n", cfg.FluxNamespace, "get", "kustomizations.kustomize.toolkit.fluxcd.io", "-o", "json")
	if err != nil {
		return err
	}
	if err := validateFluxKustomizations(fluxRaw, cfg.RequiredKustomizations, cfg.ExpectedRevision); err != nil {
		return err
	}

	certManagerRaw, err := runner.output(ctx, "cert-manager Deployment", "-n", cfg.CertManagerNamespace, "get", "deployment.apps/"+cfg.CertManagerDeployment, "-o", "json")
	if err != nil {
		return err
	}
	if err := validateDeploymentReplicasReady(certManagerRaw, "cert-manager", cfg.CertManagerDeployment); err != nil {
		return err
	}
	for _, certificate := range cfg.RequiredCertificates {
		certificateRaw, err := runner.output(ctx, "cert-manager Certificate "+certificate, "-n", cfg.OpenBaoNamespace, "get", "certificates.cert-manager.io/"+certificate, "-o", "json")
		if err != nil {
			return err
		}
		if err := validateReadyCondition(certificateRaw, "Certificate", certificate); err != nil {
			return err
		}
	}

	helmReleaseRaw, err := runner.output(ctx, "OpenBao HelmRelease", "-n", cfg.OpenBaoNamespace, "get", "helmreleases.helm.toolkit.fluxcd.io/"+cfg.OpenBaoHelmRelease, "-o", "json")
	if err != nil {
		return err
	}
	if err := validateReadyCondition(helmReleaseRaw, "HelmRelease", cfg.OpenBaoHelmRelease); err != nil {
		return err
	}

	statefulSetRaw, err := runner.output(ctx, "OpenBao StatefulSet", "-n", cfg.OpenBaoNamespace, "get", "statefulset.apps/"+cfg.OpenBaoStatefulSet, "-o", "json")
	if err != nil {
		return err
	}
	if err := validateStatefulSetRolled(statefulSetRaw, cfg.OpenBaoStatefulSet); err != nil {
		return err
	}
	replicas, err := statefulSetReplicaCount(statefulSetRaw, cfg.OpenBaoStatefulSet)
	if err != nil {
		return err
	}
	statuses := map[string]string{}
	for i := 0; i < replicas; i++ {
		pod := fmt.Sprintf("%s-%d", cfg.OpenBaoStatefulSet, i)
		statusRaw, err := runner.output(ctx, "OpenBao status "+pod, "-n", cfg.OpenBaoNamespace, "exec", pod, "-c", "openbao", "--", "bao", "status", "-format=json", "-tls-skip-verify")
		if err != nil {
			return err
		}
		statuses[pod] = statusRaw
	}
	if err := validateOpenBaoClusterStatus(statuses); err != nil {
		return err
	}

	deploymentRaw, err := runner.output(ctx, "OpenBao ops-controller Deployment", "-n", cfg.OpenBaoNamespace, "get", "deployment.apps/"+cfg.ControllerDeployment, "-o", "json")
	if err != nil {
		return err
	}
	if err := validateDeploymentReady(deploymentRaw, cfg.ControllerDeployment); err != nil {
		return err
	}

	openBaoRaw, err := runner.output(ctx, "OpenBao operation CRs", "-n", cfg.OpenBaoNamespace, "get", strings.Join(openBaoResourceTypes(), ","), "-o", "json")
	if err != nil {
		return err
	}
	if err := validateOpenBaoCRs(openBaoRaw, cfg.RequiredOpenBaoObjects); err != nil {
		return err
	}

	fmt.Printf("openbao cutover proof passed\n")
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
			return fmt.Errorf("Flux Kustomization %q Ready = %s reason=%s", name, conditionStatus(ready), conditionReason(ready))
		}
		if expectedRevision != "" && !strings.Contains(item.Status.LastAppliedRevision, expectedRevision) {
			return fmt.Errorf("Flux Kustomization %q lastAppliedRevision = %q, want it to contain %q", name, item.Status.LastAppliedRevision, expectedRevision)
		}
	}
	fmt.Printf("Flux Kustomizations ready: %s\n", strings.Join(required, ","))
	return nil
}

func validateDeploymentReady(raw string, name string) error {
	if err := validateDeploymentReplicasReady(raw, "OpenBao ops-controller", name); err != nil {
		return err
	}
	var deployment kubeObject
	if err := json.Unmarshal([]byte(raw), &deployment); err != nil {
		return fmt.Errorf("parse Deployment %q: %w", name, err)
	}
	image := containerImage(deployment.Spec.Template.Spec.Containers, "manager")
	if !strings.Contains(image, "@sha256:") {
		return fmt.Errorf("Deployment %q manager image = %q, want digest-pinned image", name, image)
	}
	fmt.Printf("OpenBao ops-controller image digest-pinned: deployment=%s image=%s\n", name, image)
	return nil
}

func validateDeploymentReplicasReady(raw string, kind string, name string) error {
	var deployment kubeObject
	if err := json.Unmarshal([]byte(raw), &deployment); err != nil {
		return fmt.Errorf("parse %s Deployment %q: %w", kind, name, err)
	}
	if deployment.Status.Replicas == 0 {
		return fmt.Errorf("%s Deployment %q has zero desired replicas", kind, name)
	}
	if deployment.Status.ReadyReplicas != deployment.Status.Replicas {
		return fmt.Errorf("%s Deployment %q readyReplicas=%d replicas=%d", kind, name, deployment.Status.ReadyReplicas, deployment.Status.Replicas)
	}
	fmt.Printf("%s Deployment ready: deployment=%s replicas=%d\n", kind, name, deployment.Status.Replicas)
	return nil
}

func validateReadyCondition(raw string, kind string, name string) error {
	var item kubeObject
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return fmt.Errorf("parse %s %q: %w", kind, name, err)
	}
	ready := conditionByType(item.Status.Conditions, "Ready")
	if ready == nil || ready.Status != "True" {
		return fmt.Errorf("%s %q Ready = %s reason=%s", kind, name, conditionStatus(ready), conditionReason(ready))
	}
	fmt.Printf("%s ready: %s\n", kind, name)
	return nil
}

func validateStatefulSetRolled(raw string, name string) error {
	replicas, err := statefulSetReplicaCount(raw, name)
	if err != nil {
		return err
	}
	var statefulSet kubeObject
	if err := json.Unmarshal([]byte(raw), &statefulSet); err != nil {
		return fmt.Errorf("parse StatefulSet %q: %w", name, err)
	}
	if statefulSet.Status.ReadyReplicas != replicas {
		return fmt.Errorf("StatefulSet %q readyReplicas=%d replicas=%d", name, statefulSet.Status.ReadyReplicas, replicas)
	}
	if statefulSet.Spec.UpdateStrategy.Type != "OnDelete" && statefulSet.Status.UpdatedReplicas != replicas {
		return fmt.Errorf("StatefulSet %q updatedReplicas=%d replicas=%d", name, statefulSet.Status.UpdatedReplicas, replicas)
	}
	if statefulSet.Status.UpdateRevision == "" {
		return fmt.Errorf("StatefulSet %q has empty rollout revisions current=%q update=%q", name, statefulSet.Status.CurrentRevision, statefulSet.Status.UpdateRevision)
	}
	if statefulSet.Spec.UpdateStrategy.Type != "OnDelete" && statefulSet.Status.CurrentRevision != statefulSet.Status.UpdateRevision {
		return fmt.Errorf("StatefulSet %q currentRevision=%q updateRevision=%q", name, statefulSet.Status.CurrentRevision, statefulSet.Status.UpdateRevision)
	}
	fmt.Printf("OpenBao StatefulSet ready: statefulSet=%s replicas=%d updatedReplicas=%d updateRevision=%s strategy=%s currentRevision=%s\n", name, replicas, statefulSet.Status.UpdatedReplicas, statefulSet.Status.UpdateRevision, statefulSet.Spec.UpdateStrategy.Type, statefulSet.Status.CurrentRevision)
	return nil
}

func statefulSetReplicaCount(raw string, name string) (int, error) {
	var statefulSet kubeObject
	if err := json.Unmarshal([]byte(raw), &statefulSet); err != nil {
		return 0, fmt.Errorf("parse StatefulSet %q: %w", name, err)
	}
	replicas := statefulSet.Spec.Replicas
	if replicas == 0 {
		replicas = statefulSet.Status.Replicas
	}
	if replicas == 0 {
		return 0, fmt.Errorf("StatefulSet %q has zero desired replicas", name)
	}
	return replicas, nil
}

func validateOpenBaoClusterStatus(rawByPod map[string]string) error {
	if len(rawByPod) == 0 {
		return errors.New("no OpenBao pod statuses were collected")
	}
	var clusterID string
	for pod, raw := range rawByPod {
		var status openBaoStatus
		if err := json.Unmarshal([]byte(raw), &status); err != nil {
			return fmt.Errorf("parse OpenBao status for %s: %w", pod, err)
		}
		if !status.Initialized {
			return fmt.Errorf("OpenBao pod %s initialized=false", pod)
		}
		if status.Sealed {
			return fmt.Errorf("OpenBao pod %s sealed=true", pod)
		}
		if !status.HAEnabled {
			return fmt.Errorf("OpenBao pod %s ha_enabled=false", pod)
		}
		if status.ClusterID == "" {
			return fmt.Errorf("OpenBao pod %s cluster_id is empty", pod)
		}
		if clusterID == "" {
			clusterID = status.ClusterID
			continue
		}
		if status.ClusterID != clusterID {
			return fmt.Errorf("OpenBao pod %s cluster_id=%s, want %s", pod, status.ClusterID, clusterID)
		}
	}
	fmt.Printf("OpenBao cluster status verified: pods=%d clusterID=%s\n", len(rawByPod), clusterID)
	return nil
}

func validateOpenBaoCRs(raw string, required []string) error {
	var list kubeList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return fmt.Errorf("parse OpenBao CRs: %w", err)
	}
	objects := map[string]kubeObject{}
	for _, item := range list.Items {
		objects[item.Kind+"/"+item.Metadata.Name] = item
	}
	for _, key := range required {
		item, ok := objects[key]
		if !ok {
			return fmt.Errorf("OpenBao CR %q is missing", key)
		}
		if err := validateConvergedCR(item); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	fmt.Printf("OpenBao CRs verified: %s\n", strings.Join(required, ","))
	return nil
}

func validateConvergedCR(item kubeObject) error {
	for _, expected := range []struct {
		condition string
		status    string
	}{
		{"Authenticated", "True"},
		{"Applied", "True"},
		{"Ready", "True"},
		{"DriftDetected", "False"},
	} {
		cond := conditionByType(item.Status.Conditions, expected.condition)
		if cond == nil {
			return fmt.Errorf("missing %s condition", expected.condition)
		}
		if cond.Status != expected.status {
			return fmt.Errorf("%s = %s reason=%s, want %s", expected.condition, cond.Status, cond.Reason, expected.status)
		}
	}
	if item.Status.LastError != "" {
		return fmt.Errorf("lastError = %q, want empty", item.Status.LastError)
	}
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

func openBaoResourceTypes() []string {
	return []string{
		"openbaoauthbackends.openbao.guardian.dev",
		"openbaokubernetesauthroles.openbao.guardian.dev",
		"openbaomounts.openbao.guardian.dev",
		"openbaomounttunes.openbao.guardian.dev",
		"openbaopolicies.openbao.guardian.dev",
	}
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

func containerImage(containers []containerSpec, name string) string {
	for _, container := range containers {
		if container.Name == name {
			return container.Image
		}
	}
	if len(containers) == 1 {
		return containers[0].Image
	}
	return ""
}
