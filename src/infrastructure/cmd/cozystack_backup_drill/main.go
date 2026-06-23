package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type componentSpec struct {
	Kind        string
	Resource    string
	BackupClass string
}

type drillConfig struct {
	Kubectl              string
	Kubeconfig           string
	RequestTimeout       string
	WaitTimeout          string
	Stage                string
	Namespace            string
	Component            componentSpec
	ApplicationName      string
	Name                 string
	RestoreTargetName    string
	CreateRestoreTarget  bool
	CleanupRestoreTarget bool
	AllowInPlaceRestore  bool
}

var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func main() {
	var cfg drillConfig
	var component string
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "30m", "timeout for backup and restore completion waits")
	flag.StringVar(&cfg.Stage, "stage", "dev", "Guardian stage: root, dev, gamma, or prod")
	flag.StringVar(&component, "component", "clickhouse", "component to drill: clickhouse or postgres")
	flag.StringVar(&cfg.ApplicationName, "application", "guardian", "Cozystack app name")
	flag.StringVar(&cfg.Name, "name", "", "BackupJob name; defaults to a UTC timestamped DNS label")
	flag.StringVar(&cfg.RestoreTargetName, "restore-target", "", "optional existing app name to restore into")
	flag.BoolVar(&cfg.CreateRestoreTarget, "create-restore-target", false, "create the restore target app from the live source app spec before restoring")
	flag.BoolVar(&cfg.CleanupRestoreTarget, "cleanup-created-restore-target", true, "delete a restore target app created by this drill before exiting")
	flag.BoolVar(&cfg.AllowInPlaceRestore, "allow-in-place-restore", false, "allow restoring into the same app name")
	flag.Parse()

	var err error
	cfg.Namespace, err = namespaceForStage(cfg.Stage)
	exitIfErr(err)
	cfg.Component, err = componentForName(component)
	exitIfErr(err)
	if cfg.Name == "" {
		cfg.Name = defaultJobName(cfg.Stage, cfg.Component.Kind, time.Now().UTC())
	}
	exitIfErr(validateConfig(cfg))

	ctx := context.Background()
	exitIfErr(runDrill(ctx, cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func namespaceForStage(stage string) (string, error) {
	switch stage {
	case "root":
		return "tenant-root", nil
	case "dev":
		return "tenant-guardiancommercial-platform-dev", nil
	case "gamma":
		return "tenant-guardiancommercial-platform-gamma", nil
	case "prod":
		return "tenant-guardiancommercial-platform-prod", nil
	default:
		return "", fmt.Errorf("stage %q is not one of root, dev, gamma, prod", stage)
	}
}

func componentForName(name string) (componentSpec, error) {
	switch strings.ToLower(name) {
	case "clickhouse":
		return componentSpec{
			Kind:        "ClickHouse",
			Resource:    "clickhouses.apps.cozystack.io",
			BackupClass: "cozy-default",
		}, nil
	case "postgres", "postgresql":
		return componentSpec{
			Kind:        "Postgres",
			Resource:    "postgreses.apps.cozystack.io",
			BackupClass: "cozy-default",
		}, nil
	case "harbor", "registry":
		return componentSpec{}, errors.New("Harbor is not a Cozystack managed-database BackupJob target; validate Harbor registry recovery with the Harbor/COSI registry smoke path")
	default:
		return componentSpec{}, fmt.Errorf("component %q is not one of clickhouse, postgres", name)
	}
}

func defaultJobName(stage, kind string, now time.Time) string {
	return fmt.Sprintf(
		"guardian-%s-%s-%s",
		stage,
		strings.ToLower(kind),
		now.Format("20060102t150405z"),
	)
}

func validateConfig(cfg drillConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	for label, value := range map[string]string{
		"application": cfg.ApplicationName,
		"name":        cfg.Name,
	} {
		if err := validateDNSLabel(label, value); err != nil {
			return err
		}
	}
	if cfg.RestoreTargetName != "" {
		if err := validateDNSLabel("restore-target", cfg.RestoreTargetName); err != nil {
			return err
		}
		if err := validateDNSLabel("restore-job", restoreJobName(cfg.Name)); err != nil {
			return err
		}
		if cfg.RestoreTargetName == cfg.ApplicationName && !cfg.AllowInPlaceRestore {
			return errors.New("--restore-target matches --application; pass --allow-in-place-restore only for an intentional in-place restore")
		}
		if cfg.RestoreTargetName == cfg.ApplicationName && cfg.CreateRestoreTarget {
			return errors.New("--create-restore-target cannot create over the source application")
		}
	}
	if cfg.CreateRestoreTarget && cfg.RestoreTargetName == "" {
		return errors.New("--create-restore-target requires --restore-target")
	}
	return nil
}

func validateDNSLabel(label, value string) error {
	if value == "" {
		return fmt.Errorf("--%s must not be empty", label)
	}
	if len(value) > 63 {
		return fmt.Errorf("--%s %q is %d bytes; Kubernetes DNS labels are limited to 63", label, value, len(value))
	}
	if !dnsLabelRE.MatchString(value) {
		return fmt.Errorf("--%s %q is not a Kubernetes DNS label", label, value)
	}
	return nil
}

func runDrill(ctx context.Context, cfg drillConfig) error {
	dir, err := os.MkdirTemp("", "guardian-cozystack-backup-drill-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	backupJobPath := filepath.Join(dir, "backupjob.yaml")
	if err := os.WriteFile(backupJobPath, []byte(backupJobManifest(cfg)), 0o600); err != nil {
		return err
	}

	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}

	fmt.Printf("guardian cozystack backup drill\n")
	fmt.Printf("stage=%s namespace=%s application=%s/%s backupClass=%s backupJob=%s\n",
		cfg.Stage,
		cfg.Namespace,
		cfg.Component.Kind,
		cfg.ApplicationName,
		cfg.Component.BackupClass,
		cfg.Name,
	)

	if err := waitAppReady(ctx, runner, "source", cfg.Component.Resource, cfg.ApplicationName, cfg.WaitTimeout); err != nil {
		return err
	}
	if cfg.RestoreTargetName != "" {
		restoreRef := cfg.Component.Resource + "/" + cfg.RestoreTargetName
		if cfg.CreateRestoreTarget {
			exists, err := runner.exists(ctx, "check restore target does not exist", restoreTargetGetArgs(restoreRef)...)
			if err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("restore target %s already exists; omit --create-restore-target to use it or choose a fresh --restore-target", restoreRef)
			}

			sourceJSON, err := runner.output(ctx, "source app json for restore target", "get", cfg.Component.Resource+"/"+cfg.ApplicationName, "-o", "json")
			if err != nil {
				return err
			}
			restoreTarget, err := restoreTargetManifestFromSource(cfg, []byte(sourceJSON))
			if err != nil {
				return err
			}
			restoreTargetPath := filepath.Join(dir, "restore-target.json")
			if err := os.WriteFile(restoreTargetPath, []byte(restoreTarget), 0o600); err != nil {
				return err
			}
			if err := runner.run(ctx, "apply restore target app", "apply", "-f", restoreTargetPath); err != nil {
				return err
			}
			if cfg.CleanupRestoreTarget {
				defer runner.bestEffort(ctx, "delete restore target app", "delete", restoreRef, "--wait=true", "--timeout="+cfg.WaitTimeout)
			}
		}
		if err := waitAppReady(ctx, runner, "restore target", cfg.Component.Resource, cfg.RestoreTargetName, cfg.WaitTimeout); err != nil {
			return err
		}
	}

	if err := runner.run(ctx, "apply BackupJob", "apply", "-f", backupJobPath); err != nil {
		return err
	}
	if err := runner.run(ctx, "wait BackupJob Succeeded", "wait", "--for=jsonpath={.status.phase}=Succeeded", "backupjobs.backups.cozystack.io/"+cfg.Name, "--timeout="+cfg.WaitTimeout); err != nil {
		runner.bestEffort(ctx, "describe failed BackupJob", "describe", "backupjobs.backups.cozystack.io/"+cfg.Name)
		runner.bestEffort(ctx, "related pods", "get", "pods", "-l", "backups.cozystack.io/owned-by.BackupJobName="+cfg.Name, "-o", "wide")
		runner.bestEffort(ctx, "related pod logs", "logs", "-l", "backups.cozystack.io/owned-by.BackupJobName="+cfg.Name, "--all-containers=true", "--tail=-1")
		return err
	}

	if err := runner.run(ctx, "BackupJob yaml", "get", "backupjobs.backups.cozystack.io/"+cfg.Name, "-o", "yaml"); err != nil {
		return err
	}
	backupName, err := runner.output(ctx, "Backup ref", "get", "backupjobs.backups.cozystack.io/"+cfg.Name, "-o", "jsonpath={.status.backupRef.name}")
	if err != nil {
		return err
	}
	backupName = strings.TrimSpace(backupName)
	if backupName == "" {
		return errors.New("BackupJob succeeded but .status.backupRef.name is empty")
	}
	if err := runner.run(ctx, "wait Backup Ready", "wait", "--for=jsonpath={.status.phase}=Ready", "backups.backups.cozystack.io/"+backupName, "--timeout="+cfg.WaitTimeout); err != nil {
		return err
	}
	if err := runner.run(ctx, "Backup yaml", "get", "backups.backups.cozystack.io/"+backupName, "-o", "yaml"); err != nil {
		return err
	}
	runner.bestEffort(ctx, "related jobs", "get", "jobs", "-l", "backups.cozystack.io/owned-by.BackupJobName="+cfg.Name, "-o", "wide")
	runner.bestEffort(ctx, "related pods", "get", "pods", "-l", "backups.cozystack.io/owned-by.BackupJobName="+cfg.Name, "-o", "wide")
	runner.bestEffort(ctx, "related pod logs", "logs", "-l", "backups.cozystack.io/owned-by.BackupJobName="+cfg.Name, "--all-containers=true", "--tail=-1")

	if cfg.RestoreTargetName == "" {
		fmt.Printf("backup drill completed: backup=%s\n", backupName)
		return nil
	}

	restoreName := restoreJobName(cfg.Name)
	restoreJobPath := filepath.Join(dir, "restorejob.yaml")
	if err := os.WriteFile(restoreJobPath, []byte(restoreJobManifest(cfg, restoreName, backupName)), 0o600); err != nil {
		return err
	}

	fmt.Printf("restoreTarget=%s/%s restoreJob=%s\n", cfg.Component.Kind, cfg.RestoreTargetName, restoreName)
	if err := runner.run(ctx, "apply RestoreJob", "apply", "-f", restoreJobPath); err != nil {
		return err
	}
	if err := runner.run(ctx, "wait RestoreJob Succeeded", "wait", "--for=jsonpath={.status.phase}=Succeeded", "restorejobs.backups.cozystack.io/"+restoreName, "--timeout="+cfg.WaitTimeout); err != nil {
		runner.bestEffort(ctx, "describe failed RestoreJob", "describe", "restorejobs.backups.cozystack.io/"+restoreName)
		return err
	}
	if err := runner.run(ctx, "RestoreJob yaml", "get", "restorejobs.backups.cozystack.io/"+restoreName, "-o", "yaml"); err != nil {
		return err
	}
	if err := waitAppReady(ctx, runner, "restored target", cfg.Component.Resource, cfg.RestoreTargetName, cfg.WaitTimeout); err != nil {
		return err
	}
	fmt.Printf("backup and restore drill completed: backup=%s restoreJob=%s\n", backupName, restoreName)
	return nil
}

func waitAppReady(ctx context.Context, runner kubectlRunner, label, resource, name, timeout string) error {
	ref := resource + "/" + name
	if err := runner.run(ctx, label+" app yaml", "get", ref, "-o", "yaml"); err != nil {
		return err
	}
	if err := runner.run(ctx, "wait "+label+" app Ready", "wait", "--for=condition=Ready", ref, "--timeout="+timeout); err != nil {
		return err
	}
	return runner.run(ctx, "wait "+label+" workloads Ready", "wait", "--for=condition=WorkloadsReady", ref, "--timeout="+timeout)
}

func restoreJobName(backupJobName string) string {
	return backupJobName + "-restore"
}

func restoreTargetGetArgs(ref string) []string {
	return []string{"get", ref}
}

func restoreTargetManifestFromSource(cfg drillConfig, sourceJSON []byte) (string, error) {
	var source map[string]interface{}
	if err := json.Unmarshal(sourceJSON, &source); err != nil {
		return "", fmt.Errorf("decode source app JSON: %w", err)
	}
	for _, field := range []string{"apiVersion", "kind", "spec"} {
		if _, ok := source[field]; !ok {
			return "", fmt.Errorf("source app JSON missing %s", field)
		}
	}
	target := map[string]interface{}{
		"apiVersion": source["apiVersion"],
		"kind":       source["kind"],
		"metadata": map[string]interface{}{
			"name":      cfg.RestoreTargetName,
			"namespace": cfg.Namespace,
			"labels": map[string]string{
				"app.kubernetes.io/part-of": "guardian",
				"guardian.dev/component":    strings.ToLower(cfg.Component.Kind),
				"guardian.dev/drill":        "cozystack-restore-target",
				"guardian.dev/stage":        cfg.Stage,
			},
		},
		"spec": source["spec"],
	}
	out, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode restore target app JSON: %w", err)
	}
	return string(out) + "\n", nil
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	requestTimeout string
	namespace      string
}

func (r kubectlRunner) baseArgs(args ...string) []string {
	out := make([]string, 0, len(args)+6)
	if r.kubeconfig != "" {
		out = append(out, "--kubeconfig", r.kubeconfig)
	}
	if r.requestTimeout != "" {
		out = append(out, "--request-timeout="+r.requestTimeout)
	}
	if r.namespace != "" {
		out = append(out, "-n", r.namespace)
	}
	out = append(out, args...)
	return out
}

func (r kubectlRunner) run(ctx context.Context, label string, args ...string) error {
	fmt.Printf("\n## %s\n", label)
	out, err := r.combinedOutput(ctx, args...)
	fmt.Print(out)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func (r kubectlRunner) bestEffort(ctx context.Context, label string, args ...string) {
	fmt.Printf("\n## %s\n", label)
	out, err := r.combinedOutput(ctx, args...)
	fmt.Print(out)
	if err != nil {
		fmt.Printf("best-effort command failed: %v\n", err)
	}
}

func (r kubectlRunner) exists(ctx context.Context, label string, args ...string) (bool, error) {
	fmt.Printf("\n## %s\n", label)
	out, err := r.combinedOutput(ctx, args...)
	fmt.Print(out)
	if err == nil {
		return true, nil
	}
	if strings.Contains(out, "NotFound") || strings.Contains(out, "not found") {
		return false, nil
	}
	return false, fmt.Errorf("%s: %w", label, err)
}

func (r kubectlRunner) output(ctx context.Context, label string, args ...string) (string, error) {
	fmt.Printf("\n## %s\n", label)
	out, err := r.combinedOutput(ctx, args...)
	fmt.Print(out)
	if err != nil {
		return "", fmt.Errorf("%s: %w", label, err)
	}
	return out, nil
}

func (r kubectlRunner) combinedOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.baseArgs(args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func backupJobManifest(cfg drillConfig) string {
	return fmt.Sprintf(`apiVersion: backups.cozystack.io/v1alpha1
kind: BackupJob
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/part-of: guardian
    guardian.dev/drill: cozystack-backup
    guardian.dev/stage: %s
spec:
  applicationRef:
    apiGroup: apps.cozystack.io
    kind: %s
    name: %s
  backupClassName: %s
`,
		cfg.Name,
		cfg.Namespace,
		cfg.Stage,
		cfg.Component.Kind,
		cfg.ApplicationName,
		cfg.Component.BackupClass,
	)
}

func restoreJobManifest(cfg drillConfig, restoreName, backupName string) string {
	return fmt.Sprintf(`apiVersion: backups.cozystack.io/v1alpha1
kind: RestoreJob
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/part-of: guardian
    guardian.dev/drill: cozystack-restore
    guardian.dev/stage: %s
spec:
  backupRef:
    name: %s
  targetApplicationRef:
    apiGroup: apps.cozystack.io
    kind: %s
    name: %s
`,
		restoreName,
		cfg.Namespace,
		cfg.Stage,
		backupName,
		cfg.Component.Kind,
		cfg.RestoreTargetName,
	)
}
