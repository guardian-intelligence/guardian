package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const postgresBenchImage = "ghcr.io/cloudnative-pg/postgresql@sha256:6f64c83d80def98ab5b61bf36b1bbecea01dede382eef781dd9d1638b0d840c8"

type observabilityConfig struct {
	Kubectl                 string
	Kubeconfig              string
	RequestTimeout          string
	WaitTimeout             string
	PollTimeout             time.Duration
	PollInterval            time.Duration
	PortForwardReadyWait    time.Duration
	Stage                   string
	Namespace               string
	ApplicationName         string
	Name                    string
	TTLSecondsAfterFinished string
	PgbenchScale            string
	PgbenchClients          string
	PgbenchJobs             string
	PgbenchDurationSeconds  string
	SkipLoad                bool
}

type prometheusResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []prometheusResult `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

type prometheusResult struct {
	Metric map[string]string `json:"metric"`
	Value  []any             `json:"value"`
}

type queryCheck struct {
	label string
	query string
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	requestTimeout string
	namespace      string
}

type portForward struct {
	cmd *exec.Cmd
}

var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func main() {
	var cfg observabilityConfig
	var pollTimeout string
	var pollInterval string
	var portForwardReadyWait string
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "20m", "timeout for readiness and load Job completion")
	flag.StringVar(&pollTimeout, "poll-timeout", "3m", "timeout waiting for VictoriaMetrics and VictoriaLogs ingestion")
	flag.StringVar(&pollInterval, "poll-interval", "10s", "poll interval for observability queries")
	flag.StringVar(&portForwardReadyWait, "port-forward-ready-timeout", "10s", "timeout waiting for kubectl port-forward readiness")
	flag.StringVar(&cfg.Stage, "stage", "root", "Guardian bootstrap stage: root")
	flag.StringVar(&cfg.ApplicationName, "application", "guardian", "Cozystack Postgres app name")
	flag.StringVar(&cfg.Name, "name", "", "load Job name; defaults to a UTC timestamped DNS label")
	flag.StringVar(&cfg.TTLSecondsAfterFinished, "ttl-seconds-after-finished", "86400", "Kubernetes Job ttlSecondsAfterFinished")
	flag.StringVar(&cfg.PgbenchScale, "pgbench-scale", "10", "pgbench scale factor")
	flag.StringVar(&cfg.PgbenchClients, "pgbench-clients", "4", "pgbench client count")
	flag.StringVar(&cfg.PgbenchJobs, "pgbench-jobs", "2", "pgbench worker thread count")
	flag.StringVar(&cfg.PgbenchDurationSeconds, "pgbench-duration-seconds", "30", "pgbench run duration in seconds")
	flag.BoolVar(&cfg.SkipLoad, "skip-load", false, "skip creating the pgbench Job and only verify observability for --name")
	flag.Parse()

	var err error
	cfg.Namespace, err = namespaceForStage(cfg.Stage)
	exitIfErr(err)
	cfg.PollTimeout, err = time.ParseDuration(pollTimeout)
	exitIfErr(err)
	cfg.PollInterval, err = time.ParseDuration(pollInterval)
	exitIfErr(err)
	cfg.PortForwardReadyWait, err = time.ParseDuration(portForwardReadyWait)
	exitIfErr(err)
	if cfg.Name == "" {
		cfg.Name = defaultJobName(cfg.Stage, time.Now().UTC())
	}
	exitIfErr(validateConfig(cfg))
	exitIfErr(runDrill(context.Background(), cfg))
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

func defaultJobName(stage string, now time.Time) string {
	return fmt.Sprintf("guardian-%s-observability-%s", stage, now.Format("20060102t150405z"))
}

func validateConfig(cfg observabilityConfig) error {
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
		"ttl-seconds-after-finished": cfg.TTLSecondsAfterFinished,
		"pgbench-scale":              cfg.PgbenchScale,
		"pgbench-clients":            cfg.PgbenchClients,
		"pgbench-jobs":               cfg.PgbenchJobs,
		"pgbench-duration-seconds":   cfg.PgbenchDurationSeconds,
	} {
		if !isPositiveInteger(value) {
			return fmt.Errorf("--%s must be a positive integer", label)
		}
	}
	if cfg.PollTimeout <= 0 {
		return errors.New("--poll-timeout must be positive")
	}
	if cfg.PollInterval <= 0 {
		return errors.New("--poll-interval must be positive")
	}
	if cfg.PortForwardReadyWait <= 0 {
		return errors.New("--port-forward-ready-timeout must be positive")
	}
	if cfg.SkipLoad && cfg.Name == "" {
		return errors.New("--name is required with --skip-load")
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

func runDrill(ctx context.Context, cfg observabilityConfig) error {
	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}

	fmt.Printf("guardian cozystack observability drill\n")
	fmt.Printf("stage=%s namespace=%s postgres=%s job=%s\n", cfg.Stage, cfg.Namespace, cfg.ApplicationName, cfg.Name)

	if err := waitObservabilityReady(ctx, runner, cfg.WaitTimeout); err != nil {
		return err
	}
	if err := waitHubbleReady(ctx, runner, cfg.WaitTimeout); err != nil {
		return err
	}
	if err := waitPostgresReady(ctx, runner, cfg.ApplicationName, cfg.WaitTimeout); err != nil {
		return err
	}
	if !cfg.SkipLoad {
		if err := runLoadJob(ctx, runner, cfg); err != nil {
			return err
		}
	}
	if err := verifyVictoriaMetrics(ctx, cfg); err != nil {
		return err
	}
	if err := verifyVictoriaLogs(ctx, cfg); err != nil {
		return err
	}
	fmt.Printf("observability drill completed: job=%s\n", cfg.Name)
	return nil
}

func waitObservabilityReady(ctx context.Context, runner kubectlRunner, timeout string) error {
	if err := runner.run(ctx, "Monitoring app", "get", "monitorings.apps.cozystack.io/monitoring"); err != nil {
		return err
	}
	for _, args := range [][]string{
		{"wait", "--for=condition=Ready", "monitorings.apps.cozystack.io/monitoring", "--timeout=" + timeout},
		{"wait", "--for=condition=WorkloadsReady", "monitorings.apps.cozystack.io/monitoring", "--timeout=" + timeout},
		{"get", "service/vmselect-shortterm"},
		{"get", "service/vlselect-generic"},
	} {
		if err := runner.run(ctx, strings.Join(args, " "), args...); err != nil {
			return err
		}
	}
	return nil
}

func waitHubbleReady(ctx context.Context, runner kubectlRunner, timeout string) error {
	hubbleRunner := runner
	hubbleRunner.namespace = "cozy-cilium"
	for _, args := range [][]string{
		{"get", "service/hubble-metrics"},
		{"get", "service/hubble-relay"},
		{"get", "service/hubble-ui"},
		{"get", "servicemonitors.monitoring.coreos.com/hubble"},
		{"get", "servicemonitors.monitoring.coreos.com/hubble-relay"},
		{"wait", "--for=condition=Available", "deployment.apps/hubble-relay", "--timeout=" + timeout},
		{"wait", "--for=condition=Available", "deployment.apps/hubble-ui", "--timeout=" + timeout},
	} {
		if err := hubbleRunner.run(ctx, strings.Join(args, " "), args...); err != nil {
			return err
		}
	}
	return nil
}

func waitPostgresReady(ctx context.Context, runner kubectlRunner, app, timeout string) error {
	ref := "postgreses.apps.cozystack.io/" + app
	if err := runner.run(ctx, "Postgres app", "get", ref); err != nil {
		return err
	}
	if err := runner.run(ctx, "wait Postgres app Ready", "wait", "--for=condition=Ready", ref, "--timeout="+timeout); err != nil {
		return err
	}
	return runner.run(ctx, "wait Postgres workloads Ready", "wait", "--for=condition=WorkloadsReady", ref, "--timeout="+timeout)
}

func runLoadJob(ctx context.Context, runner kubectlRunner, cfg observabilityConfig) error {
	dir, err := os.MkdirTemp("", "guardian-cozystack-observability-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	manifestPath := filepath.Join(dir, "job.yaml")
	if err := os.WriteFile(manifestPath, []byte(postgresJobManifest(cfg)), 0o600); err != nil {
		return err
	}

	if err := runner.run(ctx, "apply observability load Job", "apply", "-f", manifestPath); err != nil {
		return err
	}
	if err := runner.run(ctx, "wait observability load Job Complete", "wait", "--for=condition=Complete", "job/"+cfg.Name, "--timeout="+cfg.WaitTimeout); err != nil {
		runner.bestEffort(ctx, "describe failed observability load Job", "describe", "job/"+cfg.Name)
		runner.bestEffort(ctx, "observability load Job pods", "get", "pods", "-l", "job-name="+cfg.Name, "-o", "wide")
		runner.bestEffort(ctx, "observability load Job logs", "logs", "job/"+cfg.Name, "--all-containers=true", "--tail=-1")
		return err
	}
	if err := runner.run(ctx, "observability load Job yaml", "get", "job/"+cfg.Name, "-o", "yaml"); err != nil {
		return err
	}
	runner.bestEffort(ctx, "observability load Job pods", "get", "pods", "-l", "job-name="+cfg.Name, "-o", "wide")
	runner.bestEffort(ctx, "observability load Job logs", "logs", "job/"+cfg.Name, "--all-containers=true", "--tail=-1")
	return nil
}

func postgresJobManifest(cfg observabilityConfig) string {
	release := "postgres-" + cfg.ApplicationName
	loadDB := strings.ReplaceAll(cfg.Name, "-", "_")
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/part-of: guardian
    guardian.dev/drill: observability
    guardian.dev/stage: %s
    guardian.dev/component: postgres
spec:
  backoffLimit: 0
  ttlSecondsAfterFinished: %s
  template:
    metadata:
      labels:
        app.kubernetes.io/part-of: guardian
        guardian.dev/drill: observability
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
              echo "guardian-observability-drill job=$JOB_NAME phase=start"
              pg_isready --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" --dbname postgres
              cleanup() {
                dropdb --if-exists --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" "$PGBENCH_DATABASE"
              }
              trap cleanup EXIT
              cleanup
              createdb --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" "$PGBENCH_DATABASE"
              pgbench --initialize --scale "$PGBENCH_SCALE" --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" "$PGBENCH_DATABASE"
              pgbench --client "$PGBENCH_CLIENTS" --jobs "$PGBENCH_JOBS" --time "$PGBENCH_DURATION_SECONDS" --progress 10 --host "$PGHOST" --port "$PGPORT" --username "$PGUSER" "$PGBENCH_DATABASE"
              echo "guardian-observability-drill job=$JOB_NAME phase=complete"
          env:
            - name: HOME
              value: /tmp
            - name: JOB_NAME
              value: %s
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
		cfg.Name,
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

func verifyVictoriaMetrics(ctx context.Context, cfg observabilityConfig) error {
	port, err := freeLocalPort()
	if err != nil {
		return err
	}
	forward, err := startPortForward(ctx, cfg, "vmselect-shortterm", port, 8481)
	if err != nil {
		return err
	}
	defer forward.Stop()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d/select/0/prometheus/api/v1/query", port)
	for _, check := range victoriaMetricsQueries(cfg) {
		if err := waitPrometheusQuery(ctx, baseURL, check.label, check.query, cfg.PollTimeout, cfg.PollInterval); err != nil {
			return err
		}
	}
	return nil
}

func victoriaMetricsQueries(cfg observabilityConfig) []queryCheck {
	return []queryCheck{
		{
			label: "Postgres scrape targets in VictoriaMetrics",
			query: fmt.Sprintf(`sum(up{namespace=%q,job=%q})`, cfg.Namespace, cfg.Namespace+"/postgres-"+cfg.ApplicationName),
		},
		{
			label: "CNPG collector health in VictoriaMetrics",
			query: fmt.Sprintf(`sum(cnpg_collector_up{namespace=%q,job=%q})`, cfg.Namespace, cfg.Namespace+"/postgres-"+cfg.ApplicationName),
		},
		{
			label: "pgbench Job success in VictoriaMetrics",
			query: fmt.Sprintf(`sum(kube_job_status_succeeded{namespace=%q,job_name=%q})`, cfg.Namespace, cfg.Name),
		},
		{
			label: "CNPG transaction counters in VictoriaMetrics",
			query: fmt.Sprintf(`count(cnpg_pg_stat_database_xact_commit{namespace=%q,job=%q})`, cfg.Namespace, cfg.Namespace+"/postgres-"+cfg.ApplicationName),
		},
		{
			label: "Hubble flow metrics in VictoriaMetrics",
			query: `sum(hubble_flows_processed_total)`,
		},
		{
			label: "Hubble TCP metrics in VictoriaMetrics",
			query: `sum(hubble_tcp_flags_total)`,
		},
	}
}

func waitPrometheusQuery(ctx context.Context, endpoint, label, query string, timeout, interval time.Duration) error {
	fmt.Printf("\n## %s\nquery=%s\n", label, query)
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last string
	for {
		raw, ok, err := prometheusQuery(deadline, endpoint, query)
		if err == nil {
			last = raw
			if ok {
				fmt.Printf("matched %s: %s\n", label, truncate(raw, 700))
				return nil
			}
		} else {
			last = err.Error()
		}

		timer := time.NewTimer(interval)
		select {
		case <-deadline.Done():
			timer.Stop()
			return fmt.Errorf("%s did not return a positive result within %s; last response: %s", label, timeout, truncate(last, 900))
		case <-timer.C:
		}
	}
}

func prometheusQuery(ctx context.Context, endpoint, query string) (string, bool, error) {
	values := url.Values{}
	values.Set("query", query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return "", false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false, err
	}
	raw := string(body)
	if resp.StatusCode != http.StatusOK {
		return raw, false, fmt.Errorf("prometheus query status %s: %s", resp.Status, truncate(raw, 900))
	}
	var parsed prometheusResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return raw, false, err
	}
	if parsed.Status != "success" {
		return raw, false, fmt.Errorf("prometheus query status %q: %s", parsed.Status, parsed.Error)
	}
	for _, result := range parsed.Data.Result {
		if prometheusValuePositive(result.Value) {
			return raw, true, nil
		}
	}
	return raw, false, nil
}

func prometheusValuePositive(value []any) bool {
	if len(value) < 2 {
		return false
	}
	s, ok := value[1].(string)
	if !ok {
		return false
	}
	f, err := strconv.ParseFloat(s, 64)
	return err == nil && f > 0
}

func verifyVictoriaLogs(ctx context.Context, cfg observabilityConfig) error {
	port, err := freeLocalPort()
	if err != nil {
		return err
	}
	forward, err := startPortForward(ctx, cfg, "vlselect-generic", port, 9471)
	if err != nil {
		return err
	}
	defer forward.Stop()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d/select/logsql/query", port)
	queries := []struct {
		label string
		query string
	}{
		{
			label: "pgbench Job logs in VictoriaLogs",
			query: fmt.Sprintf(`kubernetes_namespace_name:%s kubernetes_pod_name:%s-* _msg:guardian-observability-drill`, cfg.Namespace, cfg.Name),
		},
		{
			label: "Postgres pod logs in VictoriaLogs",
			query: fmt.Sprintf(`kubernetes_namespace_name:%s kubernetes_pod_name:postgres-%s-*`, cfg.Namespace, cfg.ApplicationName),
		},
	}
	for _, check := range queries {
		if err := waitVictoriaLogsQuery(ctx, baseURL, check.label, check.query, cfg.PollTimeout, cfg.PollInterval); err != nil {
			return err
		}
	}
	return nil
}

func waitVictoriaLogsQuery(ctx context.Context, endpoint, label, query string, timeout, interval time.Duration) error {
	fmt.Printf("\n## %s\nquery=%s\n", label, query)
	deadline, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last string
	for {
		raw, ok, err := victoriaLogsQuery(deadline, endpoint, query)
		if err == nil {
			last = raw
			if ok {
				fmt.Printf("matched %s: %s\n", label, truncate(raw, 700))
				return nil
			}
		} else {
			last = err.Error()
		}

		timer := time.NewTimer(interval)
		select {
		case <-deadline.Done():
			timer.Stop()
			return fmt.Errorf("%s returned no log records within %s; last response: %s", label, timeout, truncate(last, 900))
		case <-timer.C:
		}
	}
}

func victoriaLogsQuery(ctx context.Context, endpoint, query string) (string, bool, error) {
	values := url.Values{}
	values.Set("query", query)
	values.Set("limit", "1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+values.Encode(), nil)
	if err != nil {
		return "", false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", false, err
	}
	raw := string(body)
	if resp.StatusCode != http.StatusOK {
		return raw, false, fmt.Errorf("victorialogs query status %s: %s", resp.Status, truncate(raw, 900))
	}
	return raw, strings.TrimSpace(raw) != "", nil
}

func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func startPortForward(ctx context.Context, cfg observabilityConfig, service string, localPort, remotePort int) (*portForward, error) {
	args := kubectlArgs(cfg, "-n", cfg.Namespace, "port-forward", "--address", "127.0.0.1", "svc/"+service, fmt.Sprintf("%d:%d", localPort, remotePort))
	cmd := exec.CommandContext(ctx, cfg.Kubectl, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ready := make(chan string, 1)
	done := make(chan string, 2)
	readPortForwardOutput := func(scanner *bufio.Scanner) {
		var buf bytes.Buffer
		for scanner.Scan() {
			line := scanner.Text()
			buf.WriteString(line)
			buf.WriteByte('\n')
			if strings.Contains(line, "Forwarding from") {
				select {
				case ready <- line:
				default:
				}
			}
		}
		done <- buf.String()
	}
	go readPortForwardOutput(bufio.NewScanner(stdout))
	go readPortForwardOutput(bufio.NewScanner(stderr))

	timer := time.NewTimer(cfg.PortForwardReadyWait)
	defer timer.Stop()
	select {
	case <-ready:
		fmt.Printf("kubectl port-forward %s established on 127.0.0.1:%d\n", service, localPort)
		return &portForward{cmd: cmd}, nil
	case <-timer.C:
		_ = cmd.Process.Kill()
		output := drainOutput(done)
		return nil, fmt.Errorf("timed out waiting for %s port-forward readiness: %s", service, output)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return nil, ctx.Err()
	}
}

func drainOutput(done chan string) string {
	var out strings.Builder
	for i := 0; i < cap(done); i++ {
		select {
		case value := <-done:
			out.WriteString(value)
		default:
		}
	}
	return out.String()
}

func (p *portForward) Stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_ = p.cmd.Wait()
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

func kubectlArgs(cfg observabilityConfig, args ...string) []string {
	out := []string{}
	if cfg.Kubeconfig != "" {
		out = append(out, "--kubeconfig", cfg.Kubeconfig)
	}
	if cfg.RequestTimeout != "" {
		out = append(out, "--request-timeout="+cfg.RequestTimeout)
	}
	out = append(out, args...)
	return out
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}
