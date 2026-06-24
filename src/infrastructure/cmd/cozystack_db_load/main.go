package main

import (
	"bytes"
	"context"
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

const (
	postgresBenchImage   = "ghcr.io/cloudnative-pg/postgresql@sha256:6f64c83d80def98ab5b61bf36b1bbecea01dede382eef781dd9d1638b0d840c8"
	clickhouseBenchImage = "clickhouse/clickhouse-server@sha256:cc8c5bf275148b2de01a31e8fd6b55ba1ba2b0d3d08c23fafcb25b06e3c5dec5"
)

type dbLoadConfig struct {
	Kubectl                   string
	Kubeconfig                string
	RequestTimeout            string
	WaitTimeout               string
	Stage                     string
	Namespace                 string
	Component                 string
	ApplicationName           string
	Name                      string
	TTLSecondsAfterFinished   string
	PgbenchScale              string
	PgbenchClients            string
	PgbenchJobs               string
	PgbenchDurationSeconds    string
	ClickHouseConcurrency     string
	ClickHouseIterations      string
	ClickHouseQuery           string
	ClickHouseDurationSeconds string
}

var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func main() {
	var cfg dbLoadConfig
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "20m", "timeout for load Job completion")
	flag.StringVar(&cfg.Stage, "stage", "root", "Guardian bootstrap stage: root")
	flag.StringVar(&cfg.Component, "component", "postgres", "database component to load: postgres or clickhouse")
	flag.StringVar(&cfg.ApplicationName, "application", "guardian", "Cozystack app name")
	flag.StringVar(&cfg.Name, "name", "", "Job name; defaults to a UTC timestamped DNS label")
	flag.StringVar(&cfg.TTLSecondsAfterFinished, "ttl-seconds-after-finished", "86400", "Kubernetes Job ttlSecondsAfterFinished")
	flag.StringVar(&cfg.PgbenchScale, "pgbench-scale", "10", "pgbench scale factor")
	flag.StringVar(&cfg.PgbenchClients, "pgbench-clients", "4", "pgbench client count")
	flag.StringVar(&cfg.PgbenchJobs, "pgbench-jobs", "2", "pgbench worker thread count")
	flag.StringVar(&cfg.PgbenchDurationSeconds, "pgbench-duration-seconds", "60", "pgbench run duration in seconds")
	flag.StringVar(&cfg.ClickHouseConcurrency, "clickhouse-concurrency", "4", "clickhouse-benchmark concurrency")
	flag.StringVar(&cfg.ClickHouseIterations, "clickhouse-iterations", "100", "clickhouse-benchmark iterations")
	flag.StringVar(&cfg.ClickHouseDurationSeconds, "clickhouse-duration-seconds", "60", "clickhouse-benchmark max duration in seconds")
	flag.StringVar(&cfg.ClickHouseQuery, "clickhouse-query", "SELECT sum(number) FROM numbers(1000000)", "clickhouse-benchmark query")
	flag.Parse()

	var err error
	cfg.Namespace, err = namespaceForStage(cfg.Stage)
	exitIfErr(err)
	cfg.Component, err = componentName(cfg.Component)
	exitIfErr(err)
	if cfg.Name == "" {
		cfg.Name = defaultJobName(cfg.Stage, cfg.Component, time.Now().UTC())
	}
	exitIfErr(validateConfig(cfg))
	exitIfErr(runLoad(context.Background(), cfg))
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
	default:
		return "", fmt.Errorf("stage %q is not root", stage)
	}
}

func componentName(component string) (string, error) {
	switch strings.ToLower(component) {
	case "postgres", "postgresql", "cnpg":
		return "postgres", nil
	case "clickhouse", "ch":
		return "clickhouse", nil
	default:
		return "", fmt.Errorf("component %q is not one of postgres, clickhouse", component)
	}
}

func componentResource(component string) string {
	if component == "clickhouse" {
		return "clickhouses.apps.cozystack.io"
	}
	return "postgreses.apps.cozystack.io"
}

func defaultJobName(stage, component string, now time.Time) string {
	return fmt.Sprintf(
		"guardian-%s-%s-load-%s",
		stage,
		component,
		now.Format("20060102t150405z"),
	)
}

func validateConfig(cfg dbLoadConfig) error {
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
	for label, value := range map[string]string{
		"ttl-seconds-after-finished":  cfg.TTLSecondsAfterFinished,
		"pgbench-scale":               cfg.PgbenchScale,
		"pgbench-clients":             cfg.PgbenchClients,
		"pgbench-jobs":                cfg.PgbenchJobs,
		"pgbench-duration-seconds":    cfg.PgbenchDurationSeconds,
		"clickhouse-concurrency":      cfg.ClickHouseConcurrency,
		"clickhouse-iterations":       cfg.ClickHouseIterations,
		"clickhouse-duration-seconds": cfg.ClickHouseDurationSeconds,
	} {
		if !isPositiveInteger(value) {
			return fmt.Errorf("--%s must be a positive integer", label)
		}
	}
	if strings.TrimSpace(cfg.ClickHouseQuery) == "" {
		return errors.New("--clickhouse-query must not be empty")
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

func isPositiveInteger(value string) bool {
	if value == "" {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return value != "0"
}

func runLoad(ctx context.Context, cfg dbLoadConfig) error {
	dir, err := os.MkdirTemp("", "guardian-cozystack-db-load-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	manifestPath := filepath.Join(dir, "job.yaml")
	if err := os.WriteFile(manifestPath, []byte(jobManifest(cfg)), 0o600); err != nil {
		return err
	}

	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}

	fmt.Printf("guardian cozystack db load\n")
	fmt.Printf("stage=%s namespace=%s component=%s application=%s job=%s\n",
		cfg.Stage,
		cfg.Namespace,
		cfg.Component,
		cfg.ApplicationName,
		cfg.Name,
	)

	if err := waitAppReady(ctx, runner, cfg.Component, componentResource(cfg.Component), cfg.ApplicationName, cfg.WaitTimeout); err != nil {
		return err
	}

	if err := runner.run(ctx, "apply load Job", "apply", "-f", manifestPath); err != nil {
		return err
	}
	if err := runner.run(ctx, "wait load Job Complete", "wait", "--for=condition=Complete", "job/"+cfg.Name, "--timeout="+cfg.WaitTimeout); err != nil {
		runner.bestEffort(ctx, "describe failed load Job", "describe", "job/"+cfg.Name)
		runner.bestEffort(ctx, "load Job pods", "get", "pods", "-l", "job-name="+cfg.Name, "-o", "wide")
		runner.bestEffort(ctx, "load Job logs", "logs", "job/"+cfg.Name, "--all-containers=true", "--tail=-1")
		return err
	}
	if err := runner.run(ctx, "load Job yaml", "get", "job/"+cfg.Name, "-o", "yaml"); err != nil {
		return err
	}
	runner.bestEffort(ctx, "load Job pods", "get", "pods", "-l", "job-name="+cfg.Name, "-o", "wide")
	runner.bestEffort(ctx, "load Job logs", "logs", "job/"+cfg.Name, "--all-containers=true", "--tail=-1")
	fmt.Printf("db load completed: job=%s\n", cfg.Name)
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

func jobManifest(cfg dbLoadConfig) string {
	if cfg.Component == "clickhouse" {
		return clickHouseJobManifest(cfg)
	}
	return postgresJobManifest(cfg)
}

func postgresJobManifest(cfg dbLoadConfig) string {
	release := "postgres-" + cfg.ApplicationName
	loadDB := strings.ReplaceAll(cfg.Name, "-", "_")
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/part-of: guardian
    guardian.dev/drill: db-load
    guardian.dev/stage: %s
    guardian.dev/component: postgres
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: %s
  template:
    metadata:
      labels:
        app.kubernetes.io/part-of: guardian
        guardian.dev/drill: db-load
        guardian.dev/stage: %s
        guardian.dev/component: postgres
    spec:
      restartPolicy: Never
      securityContext:
        runAsNonRoot: true
        runAsUser: 26
        runAsGroup: 26
        fsGroup: 26
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: pgbench
          image: %s
          imagePullPolicy: IfNotPresent
          command:
            - bash
            - -ceu
            - |
              echo "== pgbench target"
              pg_isready --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" --dbname postgres
              cleanup() {
                dropdb --if-exists --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" "$PGBENCH_DATABASE"
              }
              trap cleanup EXIT
              cleanup
              createdb --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" "$PGBENCH_DATABASE"
              pgbench --initialize --scale "$PGBENCH_SCALE" --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" "$PGBENCH_DATABASE"
              pgbench --client "$PGBENCH_CLIENTS" --jobs "$PGBENCH_JOBS" --time "$PGBENCH_DURATION_SECONDS" --progress 10 --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" "$PGBENCH_DATABASE"
          env:
            - name: HOME
              value: /tmp
            - name: PGHOST
              value: %s-rw
            - name: PGPORT
              value: "5432"
            - name: PGUSER
              valueFrom:
                secretKeyRef:
                  name: %s-superuser
                  key: username
            - name: PGPASSWORD
              valueFrom:
                secretKeyRef:
                  name: %s-superuser
                  key: password
            - name: PGBENCH_DATABASE
              value: %s
            - name: PGBENCH_SCALE
              value: "%s"
            - name: PGBENCH_CLIENTS
              value: "%s"
            - name: PGBENCH_JOBS
              value: "%s"
            - name: PGBENCH_DURATION_SECONDS
              value: "%s"
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
            readOnlyRootFilesystem: true
          volumeMounts:
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: tmp
          emptyDir: {}
`,
		cfg.Name,
		cfg.Namespace,
		cfg.Stage,
		cfg.TTLSecondsAfterFinished,
		cfg.Stage,
		postgresBenchImage,
		release,
		release,
		release,
		loadDB,
		cfg.PgbenchScale,
		cfg.PgbenchClients,
		cfg.PgbenchJobs,
		cfg.PgbenchDurationSeconds,
	)
}

func clickHouseJobManifest(cfg dbLoadConfig) string {
	release := "clickhouse-" + cfg.ApplicationName
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/part-of: guardian
    guardian.dev/drill: db-load
    guardian.dev/stage: %s
    guardian.dev/component: clickhouse
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: %s
  template:
    metadata:
      labels:
        app.kubernetes.io/part-of: guardian
        guardian.dev/drill: db-load
        guardian.dev/stage: %s
        guardian.dev/component: clickhouse
    spec:
      restartPolicy: Never
      securityContext:
        runAsNonRoot: true
        runAsUser: 101
        runAsGroup: 101
        fsGroup: 101
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: clickhouse-benchmark
          image: %s
          imagePullPolicy: IfNotPresent
          command:
            - bash
            - -ceu
            - |
              echo "== clickhouse-benchmark target"
              clickhouse-client --host "$CLICKHOUSE_HOST" --port "$CLICKHOUSE_PORT" --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --query "SELECT 1"
              clickhouse-benchmark --host "$CLICKHOUSE_HOST" --port "$CLICKHOUSE_PORT" --user "$CLICKHOUSE_USER" --password "$CLICKHOUSE_PASSWORD" --concurrency "$CLICKHOUSE_CONCURRENCY" --iterations "$CLICKHOUSE_ITERATIONS" --timelimit "$CLICKHOUSE_DURATION_SECONDS" --query "$CLICKHOUSE_QUERY"
          env:
            - name: HOME
              value: /tmp
            - name: CLICKHOUSE_HOST
              value: chendpoint-%s
            - name: CLICKHOUSE_PORT
              value: "9000"
            - name: CLICKHOUSE_USER
              value: backup
            - name: CLICKHOUSE_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: %s-credentials
                  key: backup
            - name: CLICKHOUSE_CONCURRENCY
              value: "%s"
            - name: CLICKHOUSE_ITERATIONS
              value: "%s"
            - name: CLICKHOUSE_DURATION_SECONDS
              value: "%s"
            - name: CLICKHOUSE_QUERY
              value: %q
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop:
                - ALL
            readOnlyRootFilesystem: true
          volumeMounts:
            - name: tmp
              mountPath: /tmp
      volumes:
        - name: tmp
          emptyDir: {}
`,
		cfg.Name,
		cfg.Namespace,
		cfg.Stage,
		cfg.TTLSecondsAfterFinished,
		cfg.Stage,
		clickhouseBenchImage,
		release,
		release,
		cfg.ClickHouseConcurrency,
		cfg.ClickHouseIterations,
		cfg.ClickHouseDurationSeconds,
		cfg.ClickHouseQuery,
	)
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

func (r kubectlRunner) combinedOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.baseArgs(args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
