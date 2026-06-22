package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	statusPass = "pass"
	statusFail = "fail"
)

type options struct {
	kubectl    string
	outputDir  string
	kubeconfig string
	context    string
	timeout    time.Duration
}

type report struct {
	GeneratedAt string     `json:"generatedAt"`
	Checks      []check    `json:"checks"`
	Artifacts   []artifact `json:"artifacts"`
}

type check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type artifact struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type query struct {
	name     string
	args     []string
	validate func([]object) []check
}

type object map[string]any

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	failed, err := collect(context.Background(), opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if failed {
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	var opts options
	fs := flag.NewFlagSet("mgmt-evidence", flag.ContinueOnError)
	fs.StringVar(&opts.kubectl, "kubectl", "", "path to the repo-pinned kubectl binary")
	fs.StringVar(&opts.outputDir, "output-dir", "", "directory for the generated evidence report")
	fs.StringVar(&opts.kubeconfig, "kubeconfig", "", "optional kubeconfig path")
	fs.StringVar(&opts.context, "context", "", "optional kubeconfig context")
	fs.DurationVar(&opts.timeout, "timeout", 20*time.Second, "per-kubectl-command timeout")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if opts.kubectl == "" {
		return options{}, errors.New("--kubectl is required")
	}
	if opts.outputDir == "" {
		return options{}, errors.New("--output-dir is required")
	}
	if opts.timeout <= 0 {
		return options{}, errors.New("--timeout must be positive")
	}
	return opts, nil
}

func collect(ctx context.Context, opts options) (bool, error) {
	rawDir := filepath.Join(opts.outputDir, "raw")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		return false, err
	}

	rep := report{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}
	failed := false

	for _, q := range queries() {
		stdout, stderr, err := runKubectl(ctx, opts, q.args)
		stdoutPath := filepath.Join(rawDir, q.name+".json")
		stderrPath := filepath.Join(rawDir, q.name+".stderr.txt")
		if writeErr := os.WriteFile(stdoutPath, stdout, 0o644); writeErr != nil {
			return false, writeErr
		}
		if writeErr := os.WriteFile(stderrPath, stderr, 0o644); writeErr != nil {
			return false, writeErr
		}
		rep.Artifacts = append(rep.Artifacts,
			artifact{Name: q.name + " stdout", Path: relPath(opts.outputDir, stdoutPath)},
			artifact{Name: q.name + " stderr", Path: relPath(opts.outputDir, stderrPath)},
		)

		if err != nil {
			failed = true
			rep.Checks = append(rep.Checks, check{
				Name:   q.name,
				Status: statusFail,
				Detail: strings.TrimSpace(err.Error() + "\n" + string(stderr)),
			})
			continue
		}

		objects, err := parseObjects(stdout)
		if err != nil {
			failed = true
			rep.Checks = append(rep.Checks, check{Name: q.name, Status: statusFail, Detail: err.Error()})
			continue
		}
		for _, c := range q.validate(objects) {
			if c.Status != statusPass {
				failed = true
			}
			rep.Checks = append(rep.Checks, c)
		}
	}

	sort.Slice(rep.Checks, func(i, j int) bool {
		return rep.Checks[i].Name < rep.Checks[j].Name
	})

	jsonBytes, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(opts.outputDir, "management-readiness.json"), append(jsonBytes, '\n'), 0o644); err != nil {
		return false, err
	}
	if err := os.WriteFile(filepath.Join(opts.outputDir, "management-readiness.md"), []byte(markdown(rep)), 0o644); err != nil {
		return false, err
	}
	return failed, nil
}

func queries() []query {
	return []query{
		{
			name:     "nodes",
			args:     []string{"get", "nodes", "-o", "json"},
			validate: validateNodes,
		},
		{
			name:     "subnets",
			args:     []string{"get", "subnet", "ovn-default", "join", "-o", "json"},
			validate: validateSubnets,
		},
		{
			name:     "metallb",
			args:     []string{"-n", "cozy-metallb", "get", "ipaddresspools.metallb.io,l2advertisements.metallb.io", "-o", "json"},
			validate: validateMetalLB,
		},
		{
			name:     "flux",
			args:     []string{"-n", "cozy-fluxcd", "get", "gitrepositories.source.toolkit.fluxcd.io,kustomizations.kustomize.toolkit.fluxcd.io", "-o", "json"},
			validate: validateFlux,
		},
		{
			name:     "storageclasses",
			args:     []string{"get", "storageclasses.storage.k8s.io", "-o", "json"},
			validate: validateStorageClasses,
		},
		{
			name:     "tenants",
			args:     []string{"-n", "tenant-root", "get", "tenants.apps.cozystack.io", "-o", "json"},
			validate: validateTenants,
		},
		{
			name:     "root-apps",
			args:     []string{"-n", "tenant-root", "get", "postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io", "-o", "json"},
			validate: validateRootApps,
		},
		{
			name:     "dev-apps",
			args:     []string{"-n", "tenant-dev", "get", "postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io", "-o", "json"},
			validate: validateEnvApps("dev", "tenant-dev"),
		},
		{
			name:     "gamma-apps",
			args:     []string{"-n", "tenant-gamma", "get", "postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io", "-o", "json"},
			validate: validateEnvApps("gamma", "tenant-gamma"),
		},
		{
			name:     "prod-apps",
			args:     []string{"-n", "tenant-prod", "get", "postgreses.apps.cozystack.io,harbors.apps.cozystack.io,clickhouses.apps.cozystack.io", "-o", "json"},
			validate: validateEnvApps("prod", "tenant-prod"),
		},
		{
			name:     "openbao",
			args:     []string{"-n", "tenant-root", "get", "openbao", "guardian", "-o", "json"},
			validate: validateOpenBao,
		},
		{
			name:     "backup-system",
			args:     []string{"get", "packages.cozystack.io", "-o", "json"},
			validate: validateBackupSystem,
		},
		{
			name:     "backup-classes",
			args:     []string{"get", "backupclasses.backups.cozystack.io", "-o", "json"},
			validate: validateBackupClasses,
		},
		{
			name:     "backup-plans",
			args:     []string{"get", "plans.backups.cozystack.io", "-A", "-o", "json"},
			validate: validateBackupPlans,
		},
		{
			name:     "backup-jobs",
			args:     []string{"get", "backupjobs.backups.cozystack.io", "-A", "-o", "json"},
			validate: validateBackupJobs,
		},
		{
			name:     "backup-restores",
			args:     []string{"get", "backups.backups.cozystack.io,restorejobs.backups.cozystack.io", "-A", "-o", "json"},
			validate: validateBackupRestores,
		},
		{
			name:     "company-site-dev",
			args:     []string{"-n", "tenant-dev", "get", "deployment/company-site", "service/company-site", "ingress/company-site", "-o", "json"},
			validate: validateCompanySite("dev", "tenant-dev", "dev.gi.org"),
		},
		{
			name:     "company-site-gamma",
			args:     []string{"-n", "tenant-gamma", "get", "deployment/company-site", "service/company-site", "ingress/company-site", "-o", "json"},
			validate: validateCompanySite("gamma", "tenant-gamma", "gamma.gi.org"),
		},
		{
			name:     "company-site-prod",
			args:     []string{"-n", "tenant-prod", "get", "deployment/company-site", "service/company-site", "ingress/company-site", "-o", "json"},
			validate: validateCompanySite("prod", "tenant-prod", "guardianintelligence.org"),
		},
	}
}

func runKubectl(ctx context.Context, opts options, args []string) ([]byte, []byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, opts.timeout)
	defer cancel()

	kubectlArgs := []string{"--request-timeout=" + opts.timeout.String()}
	if opts.kubeconfig != "" {
		kubectlArgs = append(kubectlArgs, "--kubeconfig", opts.kubeconfig)
	}
	if opts.context != "" {
		kubectlArgs = append(kubectlArgs, "--context", opts.context)
	}
	kubectlArgs = append(kubectlArgs, args...)

	cmd := exec.CommandContext(commandCtx, opts.kubectl, kubectlArgs...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if commandCtx.Err() != nil {
		err = commandCtx.Err()
	}
	return stdout.Bytes(), stderr.Bytes(), err
}

func parseObjects(data []byte) ([]object, error) {
	var root object
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if items, ok := root["items"].([]any); ok {
		out := make([]object, 0, len(items))
		for i, item := range items {
			obj, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("items[%d] is %T, want object", i, item)
			}
			out = append(out, object(obj))
		}
		return out, nil
	}
	return []object{root}, nil
}

func validateNodes(objects []object) []check {
	checks := []check{expectCount("nodes.count", len(objects), 3)}
	for _, node := range objects {
		name := nameOf(node)
		ready := hasCondition(node, "Ready", "True")
		checks = append(checks, passFail("nodes."+name+".ready", ready, "Ready=True", "Ready condition not True"))
	}
	return checks
}

func validateSubnets(objects []object) []check {
	byName := byName(objects)
	checks := []check{}
	for _, name := range []string{"ovn-default", "join"} {
		obj, ok := byName[name]
		checks = append(checks, passFail("subnets."+name+".exists", ok, "present", "missing"))
		if ok {
			checks = append(checks, passFail("subnets."+name+".mtu", intAt(obj, "spec", "mtu") == 1362, "mtu=1362", fmt.Sprintf("mtu=%d", intAt(obj, "spec", "mtu"))))
		}
	}
	return checks
}

func validateMetalLB(objects []object) []check {
	byKindName := byKindName(objects)
	return []check{
		passFail("metallb.ipaddresspool.cozystack", byKindName["IPAddressPool/cozystack"] != nil, "present", "missing"),
		passFail("metallb.l2advertisement.cozystack", byKindName["L2Advertisement/cozystack"] != nil, "present", "missing"),
	}
}

func validateFlux(objects []object) []check {
	byKindName := byKindName(objects)
	checks := []check{}
	for _, key := range []string{
		"GitRepository/guardian",
		"Kustomization/guardian-mgmt-base",
		"Kustomization/guardian-mgmt-tenant-apps",
	} {
		obj := byKindName[key]
		checkName := "flux." + strings.ToLower(strings.ReplaceAll(key, "/", "."))
		checks = append(checks, passFail(checkName+".exists", obj != nil, "present", "missing"))
		if obj != nil {
			checks = append(checks, passFail(checkName+".ready", hasCondition(obj, "Ready", "True"), "Ready=True", "Ready condition not True"))
		}
	}
	return checks
}

func validateStorageClasses(objects []object) []check {
	defaults := []string{}
	for _, obj := range objects {
		if stringAt(obj, "metadata", "annotations", "storageclass.kubernetes.io/is-default-class") == "true" {
			defaults = append(defaults, nameOf(obj))
		}
	}
	return []check{
		passFail("storage.default.replicated", len(defaults) == 1 && defaults[0] == "replicated", "replicated is the only default StorageClass", "defaults="+strings.Join(defaults, ",")),
	}
}

func validateTenants(objects []object) []check {
	byName := byName(objects)
	checks := []check{}
	for _, env := range []string{"dev", "gamma", "prod"} {
		obj := byName[env]
		checks = append(checks, passFail("tenants."+env+".exists", obj != nil, "present", "missing"))
		if obj != nil {
			checks = append(checks, passFail("tenants."+env+".host", stringAt(obj, "spec", "host") == env+".gi.org", "host="+env+".gi.org", "host="+stringAt(obj, "spec", "host")))
		}
	}
	return checks
}

func validateRootApps(objects []object) []check {
	return validateApps("root", objects, "tenant-root", "harbor.guardianintelligence.org")
}

func validateEnvApps(env, namespace string) func([]object) []check {
	return func(objects []object) []check {
		return validateApps(env, objects, namespace, "harbor."+env+".gi.org")
	}
}

func validateApps(prefix string, objects []object, namespace, harborHost string) []check {
	byKindName := byKindName(objects)
	checks := []check{}
	for _, key := range []string{"Postgres/guardian", "Harbor/guardian", "ClickHouse/guardian"} {
		obj := byKindName[key]
		checkName := "apps." + prefix + "." + strings.ToLower(strings.Split(key, "/")[0])
		checks = append(checks, passFail(checkName+".exists", obj != nil, "present", "missing"))
		if obj != nil {
			checks = append(checks, passFail(checkName+".namespace", namespaceOf(obj) == namespace, "namespace="+namespace, "namespace="+namespaceOf(obj)))
			checks = append(checks, passFail(checkName+".storage", stringAt(obj, "spec", "storageClass") == "replicated", "storageClass=replicated", "storageClass="+stringAt(obj, "spec", "storageClass")))
		}
	}
	if harbor := byKindName["Harbor/guardian"]; harbor != nil {
		checks = append(checks, passFail("apps."+prefix+".harbor.host", stringAt(harbor, "spec", "host") == harborHost, "host="+harborHost, "host="+stringAt(harbor, "spec", "host")))
	}
	return checks
}

func validateOpenBao(objects []object) []check {
	if len(objects) != 1 {
		return []check{expectCount("openbao.count", len(objects), 1)}
	}
	bao := objects[0]
	return []check{
		passFail("openbao.name", nameOf(bao) == "guardian", "name=guardian", "name="+nameOf(bao)),
		passFail("openbao.namespace", namespaceOf(bao) == "tenant-root", "namespace=tenant-root", "namespace="+namespaceOf(bao)),
		passFail("openbao.replicas", intAt(bao, "spec", "replicas") == 3, "replicas=3", fmt.Sprintf("replicas=%d", intAt(bao, "spec", "replicas"))),
		passFail("openbao.storage", stringAt(bao, "spec", "storageClass") == "local-retain", "storageClass=local-retain", "storageClass="+stringAt(bao, "spec", "storageClass")),
	}
}

func validateBackupSystem(objects []object) []check {
	byKindName := byKindName(objects)
	checks := []check{}
	for _, name := range []string{
		"cozystack.backup-controller",
		"cozystack.backupstrategy-controller",
		"cozystack.external-secrets-operator",
		"cozystack.velero",
	} {
		obj := byKindName["Package/"+name]
		checkName := "backup.system." + strings.TrimPrefix(name, "cozystack.")
		checks = append(checks, passFail(checkName+".exists", obj != nil, "present", "missing"))
		if obj != nil {
			checks = append(checks, passFail(checkName+".ready", hasCondition(obj, "Ready", "True"), "Ready=True", "Ready condition not True"))
		}
	}
	return checks
}

func validateBackupClasses(objects []object) []check {
	checks := []check{}
	for _, kind := range backupKinds() {
		found := false
		for _, obj := range objects {
			for _, strategy := range backupClassStrategies(obj) {
				if stringAt(strategy, "application", "kind") == kind &&
					stringAt(strategy, "strategyRef", "kind") != "" &&
					stringAt(strategy, "strategyRef", "name") != "" {
					found = true
				}
			}
		}
		checks = append(checks, passFail("backup.classes."+strings.ToLower(kind), found, "mapped to strategy", "missing BackupClass strategy mapping"))
	}
	return checks
}

func validateBackupPlans(objects []object) []check {
	checks := []check{}
	for _, target := range backupTargets() {
		plan := findBackupPlan(objects, target)
		prefix := backupCheckPrefix("backup.plans", target)
		checks = append(checks, passFail(prefix+".exists", plan != nil, "present", "missing"))
		if plan != nil {
			checks = append(checks,
				passFail(prefix+".backupclass", stringAt(plan, "spec", "backupClassName") != "", "backupClassName="+stringAt(plan, "spec", "backupClassName"), "backupClassName empty"),
				passFail(prefix+".schedule", stringAt(plan, "spec", "schedule", "cron") != "", "cron="+stringAt(plan, "spec", "schedule", "cron"), "cron empty"),
			)
		}
	}
	return checks
}

func validateBackupJobs(objects []object) []check {
	checks := []check{}
	for _, target := range backupTargets() {
		checks = append(checks, passFail(
			backupCheckPrefix("backup.jobs", target)+".succeeded",
			hasSucceededBackupJob(objects, target),
			"Succeeded backup job present",
			"no Succeeded BackupJob found",
		))
	}
	return checks
}

func validateBackupRestores(objects []object) []check {
	backups := map[string]backupTarget{}
	restoreTargets := map[backupTarget]bool{}
	for _, obj := range objects {
		switch kindOf(obj) {
		case "Backup":
			target := backupTarget{
				namespace: namespaceOf(obj),
				kind:      stringAt(obj, "spec", "applicationRef", "kind"),
				name:      stringAt(obj, "spec", "applicationRef", "name"),
			}
			backups[namespaceOf(obj)+"/"+nameOf(obj)] = target
		case "RestoreJob":
			if stringAt(obj, "status", "phase") != "Succeeded" {
				continue
			}
			target := restoreTarget(obj, backups)
			if target.kind != "" && target.name != "" {
				restoreTargets[target] = true
			}
		}
	}

	checks := []check{}
	for _, target := range backupTargets() {
		checks = append(checks,
			passFail(
				backupCheckPrefix("backup.artifacts", target)+".exists",
				hasBackupArtifact(backups, target),
				"Backup artifact present",
				"no Backup artifact found",
			),
			passFail(
				backupCheckPrefix("backup.restores", target)+".succeeded",
				restoreTargets[target],
				"Succeeded restore job present",
				"no Succeeded RestoreJob found",
			),
		)
	}
	return checks
}

func validateCompanySite(env, namespace, host string) func([]object) []check {
	return func(objects []object) []check {
		byKindName := byKindName(objects)
		deploy := byKindName["Deployment/company-site"]
		service := byKindName["Service/company-site"]
		ingress := byKindName["Ingress/company-site"]

		checks := []check{
			passFail("company."+env+".deployment.exists", deploy != nil, "present", "missing"),
			passFail("company."+env+".service.exists", service != nil, "present", "missing"),
			passFail("company."+env+".ingress.exists", ingress != nil, "present", "missing"),
		}
		if deploy != nil {
			ready := intAt(deploy, "status", "readyReplicas")
			desired := intAt(deploy, "spec", "replicas")
			available := intAt(deploy, "status", "availableReplicas")
			checks = append(checks,
				passFail("company."+env+".deployment.namespace", namespaceOf(deploy) == namespace, "namespace="+namespace, "namespace="+namespaceOf(deploy)),
				passFail("company."+env+".deployment.ready", desired == 3 && ready >= 3 && available >= 3, "readyReplicas>=3 availableReplicas>=3", fmt.Sprintf("replicas=%d ready=%d available=%d", desired, ready, available)),
			)
		}
		if ingress != nil {
			checks = append(checks,
				passFail("company."+env+".ingress.namespace", namespaceOf(ingress) == namespace, "namespace="+namespace, "namespace="+namespaceOf(ingress)),
				passFail("company."+env+".ingress.host", ingressHasHost(ingress, host), "host="+host, "host missing"),
			)
		}
		return checks
	}
}

func hasCondition(obj object, condType, status string) bool {
	conditions, ok := valueAt(obj, "status", "conditions").([]any)
	if !ok {
		return false
	}
	for _, cond := range conditions {
		condition, ok := cond.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] == condType && condition["status"] == status {
			return true
		}
	}
	return false
}

func ingressHasHost(obj object, host string) bool {
	rules, ok := valueAt(obj, "spec", "rules").([]any)
	if !ok {
		return false
	}
	for _, rule := range rules {
		ruleObj, ok := rule.(map[string]any)
		if ok && ruleObj["host"] == host {
			return true
		}
	}
	return false
}

type backupTarget struct {
	namespace string
	kind      string
	name      string
}

func backupKinds() []string {
	return []string{"Postgres", "ClickHouse"}
}

func backupTargets() []backupTarget {
	targets := []backupTarget{}
	for _, namespace := range []string{"tenant-root", "tenant-dev", "tenant-gamma", "tenant-prod"} {
		for _, kind := range backupKinds() {
			targets = append(targets, backupTarget{namespace: namespace, kind: kind, name: "guardian"})
		}
	}
	return targets
}

func backupClassStrategies(obj object) []object {
	raw, ok := valueAt(obj, "spec", "strategies").([]any)
	if !ok {
		return nil
	}
	strategies := make([]object, 0, len(raw))
	for _, item := range raw {
		strategy, ok := item.(map[string]any)
		if ok {
			strategies = append(strategies, object(strategy))
		}
	}
	return strategies
}

func findBackupPlan(objects []object, target backupTarget) object {
	for _, obj := range objects {
		if kindOf(obj) != "Plan" || namespaceOf(obj) != target.namespace {
			continue
		}
		if backupAppRefMatches(obj, target) {
			return obj
		}
	}
	return nil
}

func hasSucceededBackupJob(objects []object, target backupTarget) bool {
	for _, obj := range objects {
		if kindOf(obj) != "BackupJob" || namespaceOf(obj) != target.namespace {
			continue
		}
		if stringAt(obj, "status", "phase") == "Succeeded" && backupAppRefMatches(obj, target) {
			return true
		}
	}
	return false
}

func hasBackupArtifact(backups map[string]backupTarget, target backupTarget) bool {
	for _, backup := range backups {
		if backup == target {
			return true
		}
	}
	return false
}

func restoreTarget(obj object, backups map[string]backupTarget) backupTarget {
	if stringAt(obj, "spec", "targetApplicationRef", "kind") != "" {
		return backupTarget{
			namespace: namespaceOf(obj),
			kind:      stringAt(obj, "spec", "targetApplicationRef", "kind"),
			name:      stringAt(obj, "spec", "targetApplicationRef", "name"),
		}
	}
	return backups[namespaceOf(obj)+"/"+stringAt(obj, "spec", "backupRef", "name")]
}

func backupAppRefMatches(obj object, target backupTarget) bool {
	return namespaceOf(obj) == target.namespace &&
		stringAt(obj, "spec", "applicationRef", "kind") == target.kind &&
		stringAt(obj, "spec", "applicationRef", "name") == target.name
}

func backupCheckPrefix(prefix string, target backupTarget) string {
	return prefix + "." + strings.TrimPrefix(target.namespace, "tenant-") + "." + strings.ToLower(target.kind)
}

func byName(objects []object) map[string]object {
	out := map[string]object{}
	for _, obj := range objects {
		out[nameOf(obj)] = obj
	}
	return out
}

func byKindName(objects []object) map[string]object {
	out := map[string]object{}
	for _, obj := range objects {
		out[kindOf(obj)+"/"+nameOf(obj)] = obj
	}
	return out
}

func passFail(name string, ok bool, passDetail, failDetail string) check {
	if ok {
		return check{Name: name, Status: statusPass, Detail: passDetail}
	}
	return check{Name: name, Status: statusFail, Detail: failDetail}
}

func expectCount(name string, got, want int) check {
	return passFail(name, got == want, fmt.Sprintf("count=%d", got), fmt.Sprintf("count=%d want=%d", got, want))
}

func nameOf(obj object) string {
	return stringAt(obj, "metadata", "name")
}

func namespaceOf(obj object) string {
	return stringAt(obj, "metadata", "namespace")
}

func kindOf(obj object) string {
	return stringAt(obj, "kind")
}

func stringAt(obj object, path ...string) string {
	if s, ok := valueAt(obj, path...).(string); ok {
		return s
	}
	return ""
}

func intAt(obj object, path ...string) int {
	switch v := valueAt(obj, path...).(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		if math.Trunc(v) == v {
			return int(v)
		}
	}
	return 0
}

func valueAt(obj object, path ...string) any {
	var current any = map[string]any(obj)
	for _, part := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = m[part]
	}
	return current
}

func markdown(rep report) string {
	var b strings.Builder
	b.WriteString("# Management Readiness Evidence\n\n")
	b.WriteString("Generated: `" + rep.GeneratedAt + "`\n\n")
	b.WriteString("## Checks\n\n")
	for _, c := range rep.Checks {
		b.WriteString("- `" + c.Status + "` `" + c.Name + "`: " + c.Detail + "\n")
	}
	b.WriteString("\n## Raw Artifacts\n\n")
	for _, a := range rep.Artifacts {
		b.WriteString("- `" + a.Name + "`: `" + a.Path + "`\n")
	}
	return b.String()
}

func relPath(base, path string) string {
	rel, err := filepath.Rel(base, path)
	if err != nil {
		return path
	}
	return rel
}
