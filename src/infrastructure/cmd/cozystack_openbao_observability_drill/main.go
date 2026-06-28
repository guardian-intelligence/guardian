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
	"regexp"
	"strconv"
	"strings"
	"time"
)

type drillConfig struct {
	Kubectl              string
	Kubeconfig           string
	KubeAPIServer        string
	RequestTimeout       string
	WaitTimeout          string
	PollTimeout          time.Duration
	PollInterval         time.Duration
	PortForwardReadyWait time.Duration
	Stage                string
	Namespace            string
	MonitoringNamespace  string
	StatefulSet          string
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
	kubeAPIServer  string
	requestTimeout string
	namespace      string
}

type portForward struct {
	cmd *exec.Cmd
}

var dnsLabelRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func main() {
	var cfg drillConfig
	var pollTimeout string
	var pollInterval string
	var portForwardReadyWait string
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.KubeAPIServer, "kube-api-server", "", "optional Kubernetes API server override for off-VLAN proof runs")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "10m", "timeout for readiness waits")
	flag.StringVar(&pollTimeout, "poll-timeout", "5m", "timeout waiting for VictoriaMetrics and VictoriaLogs ingestion")
	flag.StringVar(&pollInterval, "poll-interval", "10s", "poll interval for observability queries")
	flag.StringVar(&portForwardReadyWait, "port-forward-ready-timeout", "10s", "timeout waiting for kubectl port-forward readiness")
	flag.StringVar(&cfg.Stage, "stage", "root", "Guardian bootstrap stage: root")
	flag.StringVar(&cfg.Namespace, "namespace", "tenant-guardian", "OpenBao namespace")
	flag.StringVar(&cfg.MonitoringNamespace, "monitoring-namespace", "tenant-root", "Cozystack monitoring namespace")
	flag.StringVar(&cfg.StatefulSet, "statefulset", "guardian-openbao", "OpenBao StatefulSet name")
	flag.Parse()

	var err error
	cfg.PollTimeout, err = time.ParseDuration(pollTimeout)
	exitIfErr(err)
	cfg.PollInterval, err = time.ParseDuration(pollInterval)
	exitIfErr(err)
	cfg.PortForwardReadyWait, err = time.ParseDuration(portForwardReadyWait)
	exitIfErr(err)
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

func validateConfig(cfg drillConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if cfg.Stage != "root" {
		return fmt.Errorf("--stage %q is not supported; OpenBao lives in the root management cluster path", cfg.Stage)
	}
	for label, value := range map[string]string{
		"namespace":            cfg.Namespace,
		"monitoring-namespace": cfg.MonitoringNamespace,
		"statefulset":          cfg.StatefulSet,
	} {
		if err := validateDNSLabel(label, value); err != nil {
			return err
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
	openbaoRunner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		kubeAPIServer:  cfg.KubeAPIServer,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}
	monitoringRunner := openbaoRunner
	monitoringRunner.namespace = cfg.MonitoringNamespace

	fmt.Printf("guardian openbao observability drill\n")
	fmt.Printf("stage=%s namespace=%s monitoringNamespace=%s statefulset=%s\n", cfg.Stage, cfg.Namespace, cfg.MonitoringNamespace, cfg.StatefulSet)

	if err := waitMonitoringReady(ctx, monitoringRunner, cfg.WaitTimeout); err != nil {
		return err
	}
	if err := waitOpenBaoReady(ctx, openbaoRunner, cfg); err != nil {
		return err
	}
	if err := verifyVictoriaMetrics(ctx, monitoringRunner, cfg); err != nil {
		return err
	}
	if err := verifyVictoriaLogs(ctx, monitoringRunner, cfg); err != nil {
		return err
	}
	fmt.Printf("openbao observability drill completed\n")
	return nil
}

func waitMonitoringReady(ctx context.Context, runner kubectlRunner, timeout string) error {
	for _, args := range [][]string{
		{"get", "monitorings.apps.cozystack.io/monitoring"},
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

func waitOpenBaoReady(ctx context.Context, runner kubectlRunner, cfg drillConfig) error {
	for _, args := range [][]string{
		{"get", "helmreleases.helm.toolkit.fluxcd.io/guardian-openbao"},
		{"wait", "--for=condition=Ready", "helmreleases.helm.toolkit.fluxcd.io/guardian-openbao", "--timeout=" + cfg.WaitTimeout},
		{"get", "vmservicescrapes.operator.victoriametrics.com/guardian-openbao"},
		{"get", "vmservicescrapes.operator.victoriametrics.com/guardian-openbao-ops-controller"},
		{"get", "vmrules.operator.victoriametrics.com/guardian-openbao"},
		{"get", "ciliumnetworkpolicies.cilium.io/allow-vmagent-to-openbao-metrics"},
		{"get", "ciliumnetworkpolicies.cilium.io/allow-vmagent-to-openbao-ops-controller-metrics"},
		{"wait", "--for=condition=Ready", "pod", "-l", "app.kubernetes.io/name=openbao-ops-controller", "--timeout=" + cfg.WaitTimeout},
		{"wait", "--for=condition=Ready", "pod", "-l", "app.kubernetes.io/instance=guardian-openbao,app.kubernetes.io/name=openbao,component=server", "--timeout=" + cfg.WaitTimeout},
	} {
		if err := runner.run(ctx, strings.Join(args, " "), args...); err != nil {
			return err
		}
	}
	return waitStatefulSetRolled(ctx, runner, cfg)
}

func waitStatefulSetRolled(ctx context.Context, runner kubectlRunner, cfg drillConfig) error {
	deadline, cancel := context.WithTimeout(ctx, parseTimeout(cfg.WaitTimeout))
	defer cancel()
	var last string
	for {
		raw, err := runner.output(ctx, "OpenBao StatefulSet revision", "get", "statefulset.apps/"+cfg.StatefulSet, "-o", "json")
		if err == nil {
			ok, summary, err := statefulSetRolled(raw)
			if err != nil {
				last = err.Error()
			} else {
				last = summary
				if ok {
					fmt.Printf("OpenBao StatefulSet rolled: %s\n", summary)
					return nil
				}
				fmt.Printf("waiting for OpenBao StatefulSet rollout: %s\n", summary)
			}
		} else {
			last = err.Error()
		}

		timer := time.NewTimer(5 * time.Second)
		select {
		case <-deadline.Done():
			timer.Stop()
			return fmt.Errorf("OpenBao StatefulSet did not roll within %s; last status: %s", cfg.WaitTimeout, last)
		case <-timer.C:
		}
	}
}

func parseTimeout(value string) time.Duration {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 10 * time.Minute
	}
	return duration
}

func statefulSetRolled(raw string) (bool, string, error) {
	var parsed struct {
		Spec struct {
			Replicas int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			ReadyReplicas   int    `json:"readyReplicas"`
			UpdatedReplicas int    `json:"updatedReplicas"`
			CurrentRevision string `json:"currentRevision"`
			UpdateRevision  string `json:"updateRevision"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return false, "", fmt.Errorf("parse StatefulSet JSON: %w", err)
	}
	if parsed.Spec.Replicas <= 0 {
		return false, "", fmt.Errorf("StatefulSet replicas must be positive, got %d", parsed.Spec.Replicas)
	}
	summary := fmt.Sprintf("ready=%d/%d updated=%d/%d currentRevision=%s updateRevision=%s",
		parsed.Status.ReadyReplicas,
		parsed.Spec.Replicas,
		parsed.Status.UpdatedReplicas,
		parsed.Spec.Replicas,
		parsed.Status.CurrentRevision,
		parsed.Status.UpdateRevision,
	)
	return parsed.Status.ReadyReplicas == parsed.Spec.Replicas &&
		parsed.Status.UpdatedReplicas == parsed.Spec.Replicas &&
		parsed.Status.CurrentRevision != "" &&
		parsed.Status.CurrentRevision == parsed.Status.UpdateRevision, summary, nil
}

func verifyVictoriaMetrics(ctx context.Context, runner kubectlRunner, cfg drillConfig) error {
	port, err := freeLocalPort()
	if err != nil {
		return err
	}
	forward, err := startPortForward(ctx, runner, cfg, "vmselect-shortterm", port, 8481)
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

func victoriaMetricsQueries(cfg drillConfig) []queryCheck {
	return []queryCheck{
		{
			label: "OpenBao scrape targets in VictoriaMetrics",
			query: fmt.Sprintf(`sum(up{namespace=%q,job="guardian-openbao-metrics"})`, cfg.Namespace),
		},
		{
			label: "OpenBao unsealed replicas in VictoriaMetrics",
			query: fmt.Sprintf(`sum(vault_core_unsealed{namespace=%q,job="guardian-openbao-metrics"})`, cfg.Namespace),
		},
		{
			label: "OpenBao active leader in VictoriaMetrics",
			query: fmt.Sprintf(`sum(vault_core_active{namespace=%q,job="guardian-openbao-metrics"})`, cfg.Namespace),
		},
		{
			label: "OpenBao audit request metrics in VictoriaMetrics",
			query: fmt.Sprintf(`sum(vault_audit_log_request_count{namespace=%q,job="guardian-openbao-metrics"})`, cfg.Namespace),
		},
		{
			label: "OpenBao audit tailer running in VictoriaMetrics",
			query: fmt.Sprintf(`sum(kube_pod_container_status_running{namespace=%q,container="audit-log-tailer",pod=~"%s-.*"})`, cfg.Namespace, cfg.StatefulSet),
		},
		{
			label: "OpenBao ops controller scrape target in VictoriaMetrics",
			query: fmt.Sprintf(`sum(up{namespace=%q,job="openbao-ops-controller"})`, cfg.Namespace),
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

func verifyVictoriaLogs(ctx context.Context, runner kubectlRunner, cfg drillConfig) error {
	port, err := freeLocalPort()
	if err != nil {
		return err
	}
	forward, err := startPortForward(ctx, runner, cfg, "vlselect-generic", port, 9471)
	if err != nil {
		return err
	}
	defer forward.Stop()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d/select/logsql/query", port)
	for _, check := range victoriaLogsQueries(cfg) {
		if err := waitVictoriaLogsQuery(ctx, baseURL, check.label, check.query, cfg.PollTimeout, cfg.PollInterval); err != nil {
			return err
		}
	}
	return nil
}

func victoriaLogsQueries(cfg drillConfig) []queryCheck {
	return []queryCheck{
		{
			label: "OpenBao audit tailer logs in VictoriaLogs",
			query: fmt.Sprintf(`kubernetes_namespace_name:%s kubernetes_container_name:audit-log-tailer kubernetes_pod_name:%s-*`, cfg.Namespace, cfg.StatefulSet),
		},
		{
			label: "OpenBao ops controller logs in VictoriaLogs",
			query: fmt.Sprintf(`kubernetes_namespace_name:%s kubernetes_container_name:manager kubernetes_pod_name:openbao-ops-controller-*`, cfg.Namespace),
		},
	}
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

func startPortForward(ctx context.Context, runner kubectlRunner, cfg drillConfig, service string, localPort, remotePort int) (*portForward, error) {
	args := runner.baseArgs("port-forward", "--address", "127.0.0.1", "svc/"+service, fmt.Sprintf("%d:%d", localPort, remotePort))
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
		fmt.Printf("kubectl port-forward %s/%s established on 127.0.0.1:%d\n", runner.namespace, service, localPort)
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
	if r.kubeAPIServer != "" {
		out = append(out, "--server", r.kubeAPIServer)
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

func (r kubectlRunner) output(ctx context.Context, label string, args ...string) (string, error) {
	out, err := r.combinedOutput(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("%s: %w", label, err)
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

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}
