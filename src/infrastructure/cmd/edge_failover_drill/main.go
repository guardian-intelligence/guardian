package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type drillConfig struct {
	K6               string
	Script           string
	Kubectl          string
	Kubeconfig       string
	Talosctl         string
	Talosconfig      string
	TalosEndpoint    string
	NodeName         string
	NodeIP           string
	ConfirmNodeIP    string
	KubeAPIServer    string
	URL              string
	ExpectedStatuses string
	Duration         string
	Warmup           time.Duration
	Cooldown         time.Duration
	SampleIntervalMS int
	RequestTimeout   string
	K6DNS            string
	KubeTimeout      time.Duration
	Report           string
}

const defaultKubeAPIServer = "https://10.8.0.250:6443"

type nodeTarget struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

type sample struct {
	Event        string  `json:"event"`
	TimeUnixMS   int64   `json:"time_unix_ms"`
	DurationMS   float64 `json:"duration_ms"`
	Status       int     `json:"status"`
	OK           bool    `json:"ok"`
	StatusOK     bool    `json:"status_ok"`
	CFRayPresent bool    `json:"cf_ray_present"`
	Error        string  `json:"error,omitempty"`
}

type outageWindow struct {
	StartUnixMS   int64 `json:"start_unix_ms"`
	EndUnixMS     int64 `json:"end_unix_ms"`
	DurationMS    int64 `json:"duration_ms"`
	FailedSamples int   `json:"failed_samples"`
}

type nodeReport struct {
	Node                    string         `json:"node"`
	NodeIP                  string         `json:"node_ip"`
	StartedUnixMS           int64          `json:"started_unix_ms"`
	RebootRequestedUnixMS   int64          `json:"reboot_requested_unix_ms"`
	NodeNotReadyUnixMS      int64          `json:"node_not_ready_unix_ms,omitempty"`
	NodeReadyUnixMS         int64          `json:"node_ready_unix_ms,omitempty"`
	FinishedUnixMS          int64          `json:"finished_unix_ms"`
	TotalSamples            int            `json:"total_samples"`
	FailedSamples           int            `json:"failed_samples"`
	MaxOutageMS             int64          `json:"max_outage_ms"`
	OutageWindows           []outageWindow `json:"outage_windows"`
	KubernetesNodeRecovered bool           `json:"kubernetes_node_recovered"`
}

type report struct {
	GeneratedUnixMS  int64        `json:"generated_unix_ms"`
	URL              string       `json:"url"`
	ExpectedStatuses []int        `json:"expected_statuses"`
	SampleIntervalMS int          `json:"sample_interval_ms"`
	Nodes            []nodeReport `json:"nodes"`
}

func main() {
	var cfg drillConfig
	var warmup, cooldown, kubeTimeout string
	flag.StringVar(&cfg.K6, "k6", "", "path to k6")
	flag.StringVar(&cfg.Script, "script", "", "path to k6 failover script")
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.Talosctl, "talosctl", "", "path to talosctl")
	flag.StringVar(&cfg.Talosconfig, "talosconfig", "", "talosconfig for guardian-mgmt")
	flag.StringVar(&cfg.TalosEndpoint, "talos-endpoint", "206.223.228.101,45.250.254.119,206.223.228.87", "comma-separated Talos API endpoints")
	flag.StringVar(&cfg.NodeName, "node-name", "", "Kubernetes node name to watch during the drill")
	flag.StringVar(&cfg.NodeIP, "node-ip", "", "Talos node IP/address to reboot")
	flag.StringVar(&cfg.ConfirmNodeIP, "confirm-node-ip", "", "must exactly match --node-ip before rebooting a node")
	flag.StringVar(&cfg.KubeAPIServer, "kube-api-server", defaultKubeAPIServer, "Kubernetes API server used while the target node reboots")
	flag.StringVar(&cfg.URL, "url", "https://guardianintelligence.org/", "public edge URL to probe during failover")
	flag.StringVar(&cfg.ExpectedStatuses, "expected-statuses", "200", "comma-separated acceptable HTTP status codes")
	flag.StringVar(&cfg.Duration, "duration", "5m", "k6 probe duration for this node drill")
	flag.StringVar(&warmup, "warmup", "10s", "time to collect successful samples before reboot")
	flag.StringVar(&cooldown, "cooldown", "20s", "time to keep sampling after Kubernetes recovery")
	flag.IntVar(&cfg.SampleIntervalMS, "sample-interval-ms", 250, "k6 sample interval in milliseconds")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "5s", "per-request public edge timeout")
	flag.StringVar(&cfg.K6DNS, "k6-dns", "ttl=0,select=random,policy=preferIPv6", "k6 DNS resolver policy for the public edge probe")
	flag.StringVar(&kubeTimeout, "kube-timeout", "10m", "timeout for Kubernetes node state transitions")
	flag.StringVar(&cfg.Report, "report", "", "path to write JSON outage report")
	flag.Parse()

	var err error
	cfg.Warmup, err = time.ParseDuration(warmup)
	exitIfErr(err)
	cfg.Cooldown, err = time.ParseDuration(cooldown)
	exitIfErr(err)
	cfg.KubeTimeout, err = time.ParseDuration(kubeTimeout)
	exitIfErr(err)

	exitIfErr(validateConfig(cfg))
	exitIfErr(run(context.Background(), cfg))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg drillConfig) error {
	for label, value := range map[string]string{
		"k6":             cfg.K6,
		"script":         cfg.Script,
		"kubectl":        cfg.Kubectl,
		"talosctl":       cfg.Talosctl,
		"talosconfig":    cfg.Talosconfig,
		"talos-endpoint": cfg.TalosEndpoint,
		"node-name":      cfg.NodeName,
		"node-ip":        cfg.NodeIP,
		"url":            cfg.URL,
		"k6-dns":         cfg.K6DNS,
		"report":         cfg.Report,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("--%s is required", label)
		}
	}
	if cfg.ConfirmNodeIP != cfg.NodeIP {
		return errors.New("--confirm-node-ip must exactly match --node-ip before rebooting a node")
	}
	if _, err := parseStatuses(cfg.ExpectedStatuses); err != nil {
		return err
	}
	if cfg.Warmup < 0 || cfg.Cooldown < 0 {
		return errors.New("--warmup and --cooldown must be non-negative")
	}
	if cfg.SampleIntervalMS <= 0 {
		return errors.New("--sample-interval-ms must be positive")
	}
	if cfg.KubeTimeout <= 0 {
		return errors.New("--kube-timeout must be positive")
	}
	if _, err := time.ParseDuration(cfg.Duration); err != nil {
		return fmt.Errorf("--duration: %w", err)
	}
	if _, err := time.ParseDuration(cfg.RequestTimeout); err != nil {
		return fmt.Errorf("--request-timeout: %w", err)
	}
	return nil
}

func run(ctx context.Context, cfg drillConfig) error {
	statuses, _ := parseStatuses(cfg.ExpectedStatuses)
	out := report{
		GeneratedUnixMS:  time.Now().UnixMilli(),
		URL:              cfg.URL,
		ExpectedStatuses: statuses,
		SampleIntervalMS: cfg.SampleIntervalMS,
		Nodes:            make([]nodeReport, 0, 1),
	}

	node := nodeTarget{Name: cfg.NodeName, IP: cfg.NodeIP}
	nodeReport, err := runNode(ctx, cfg, node)
	out.Nodes = append(out.Nodes, nodeReport)
	if err != nil {
		_ = writeReport(cfg.Report, out)
		return err
	}
	return writeReport(cfg.Report, out)
}

func runNode(ctx context.Context, cfg drillConfig, node nodeTarget) (nodeReport, error) {
	fmt.Printf("guardian edge failover drill node=%s nodeIP=%s url=%s\n", node.Name, node.IP, cfg.URL)
	if err := waitNodeReady(ctx, cfg, node.Name, cfg.KubeTimeout); err != nil {
		return nodeReport{Node: node.Name, NodeIP: node.IP}, err
	}

	consoleFile, err := os.CreateTemp("", "guardian-edge-failover-*.jsonl")
	if err != nil {
		return nodeReport{}, err
	}
	consolePath := consoleFile.Name()
	_ = consoleFile.Close()
	defer os.Remove(consolePath)

	started := time.Now()
	cmd := k6Command(ctx, cfg, consolePath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return nodeReport{}, fmt.Errorf("start k6: %w", err)
	}

	time.Sleep(cfg.Warmup)
	rebootRequested := time.Now()
	rebootErr := rebootNode(ctx, cfg, node)
	notReadyAt, notReadyErr := waitNodeNotReady(ctx, cfg, node.Name, cfg.KubeTimeout)
	readyAt, readyErr := waitNodeReadyAt(ctx, cfg, node.Name, cfg.KubeTimeout)
	time.Sleep(cfg.Cooldown)
	k6Err := cmd.Wait()
	finished := time.Now()

	if output.Len() > 0 {
		fmt.Print(output.String())
	}

	samples, parseErr := parseSamplesFile(consolePath)
	report := summarizeNode(node, samples, started, rebootRequested, notReadyAt, readyAt, finished, readyErr == nil)
	for _, window := range report.OutageWindows {
		fmt.Printf("outage node=%s start=%d end=%d duration_ms=%d failed_samples=%d\n", node.Name, window.StartUnixMS, window.EndUnixMS, window.DurationMS, window.FailedSamples)
	}
	fmt.Printf("node=%s samples=%d failed=%d max_outage_ms=%d recovered=%t\n", report.Node, report.TotalSamples, report.FailedSamples, report.MaxOutageMS, report.KubernetesNodeRecovered)

	return report, errors.Join(rebootErr, notReadyErr, readyErr, k6Err, parseErr)
}

func k6Command(ctx context.Context, cfg drillConfig, consolePath string) *exec.Cmd {
	args := []string{
		"run",
		"--quiet",
		"--dns", cfg.K6DNS,
		"--console-output", consolePath,
		"--summary-trend-stats", "avg,min,med,p(95),p(99),max",
		cfg.Script,
	}
	cmd := exec.CommandContext(ctx, cfg.K6, args...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env,
		"EDGE_FAILOVER_URL="+cfg.URL,
		"EDGE_FAILOVER_EXPECTED_STATUSES="+cfg.ExpectedStatuses,
		"EDGE_FAILOVER_DURATION="+cfg.Duration,
		"EDGE_FAILOVER_INTERVAL_MS="+strconv.Itoa(cfg.SampleIntervalMS),
		"EDGE_FAILOVER_REQUEST_TIMEOUT="+cfg.RequestTimeout,
		"EDGE_FAILOVER_REQUEST_NAME=guardian-edge-failover",
		"GUARDIAN_SURFACE=edge",
		"GUARDIAN_STAGE=root",
	)
	return cmd
}

func rebootNode(ctx context.Context, cfg drillConfig, node nodeTarget) error {
	args := []string{
		"--talosconfig", cfg.Talosconfig,
		"--endpoints", cfg.TalosEndpoint,
		"--nodes", node.IP,
		"reboot",
		"--wait=false",
	}
	fmt.Printf("reboot requested node=%s nodeIP=%s\n", node.Name, node.IP)
	cmd := exec.CommandContext(ctx, cfg.Talosctl, args...)
	output, err := cmd.CombinedOutput()
	if len(output) > 0 {
		fmt.Print(string(output))
	}
	if err != nil {
		return fmt.Errorf("talosctl reboot %s: %w", node.Name, err)
	}
	return nil
}

func waitNodeNotReady(ctx context.Context, cfg drillConfig, node string, timeout time.Duration) (time.Time, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := nodeReadyStatus(ctx, cfg, node)
		if err != nil {
			return time.Time{}, err
		}
		if !ready {
			now := time.Now()
			fmt.Printf("node not ready node=%s unix_ms=%d\n", node, now.UnixMilli())
			return now, nil
		}
		time.Sleep(1 * time.Second)
	}
	return time.Time{}, fmt.Errorf("timed out waiting for node %s to leave Ready", node)
}

func waitNodeReady(ctx context.Context, cfg drillConfig, node string, timeout time.Duration) error {
	_, err := waitNodeReadyAt(ctx, cfg, node, timeout)
	return err
}

func waitNodeReadyAt(ctx context.Context, cfg drillConfig, node string, timeout time.Duration) (time.Time, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ready, err := nodeReadyStatus(ctx, cfg, node)
		if err != nil {
			return time.Time{}, err
		}
		if ready {
			now := time.Now()
			fmt.Printf("node ready node=%s unix_ms=%d\n", node, now.UnixMilli())
			return now, nil
		}
		time.Sleep(1 * time.Second)
	}
	return time.Time{}, fmt.Errorf("timed out waiting for node %s Ready", node)
}

func nodeReadyStatus(ctx context.Context, cfg drillConfig, node string) (bool, error) {
	args := []string{"get", "node/" + node, "-o", `jsonpath={.status.conditions[?(@.type=="Ready")].status}`}
	if cfg.Kubeconfig != "" {
		args = append([]string{"--kubeconfig", cfg.Kubeconfig}, args...)
	}
	if cfg.KubeAPIServer != "" {
		args = append([]string{"--server", kubeAPIServerURL(cfg.KubeAPIServer)}, args...)
	}
	cmd := exec.CommandContext(ctx, cfg.Kubectl, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("kubectl get node %s: %w: %s", node, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)) == "True", nil
}

func kubeAPIServerURL(address string) string {
	address = strings.TrimSpace(address)
	if strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://") {
		return address
	}
	if strings.Contains(address, ":") {
		return "https://" + address
	}
	return "https://" + address + ":6443"
}

func parseSamplesFile(path string) ([]sample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var samples []sample
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		payload, ok := samplePayload(scanner.Text())
		if !ok {
			continue
		}
		var s sample
		if err := json.Unmarshal([]byte(payload), &s); err != nil {
			return nil, err
		}
		if s.Event == "guardian_edge_failover_sample" {
			samples = append(samples, s)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(samples) == 0 {
		return nil, errors.New("k6 produced no guardian_edge_failover_sample lines")
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].TimeUnixMS < samples[j].TimeUnixMS })
	return samples, nil
}

func samplePayload(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "{") {
		return line, true
	}
	index := strings.Index(line, "msg=")
	if index < 0 {
		return "", false
	}
	value := line[index+len("msg="):]
	if !strings.HasPrefix(value, `"`) {
		return "", false
	}
	end := 1
	escaped := false
	for ; end < len(value); end++ {
		switch {
		case escaped:
			escaped = false
		case value[end] == '\\':
			escaped = true
		case value[end] == '"':
			unquoted, err := strconv.Unquote(value[:end+1])
			return unquoted, err == nil
		}
	}
	return "", false
}

func summarizeNode(node nodeTarget, samples []sample, started, rebootRequested, notReadyAt, readyAt, finished time.Time, recovered bool) nodeReport {
	windows, failed := outageWindows(samples)
	maxOutage := int64(0)
	for _, window := range windows {
		if window.DurationMS > maxOutage {
			maxOutage = window.DurationMS
		}
	}
	report := nodeReport{
		Node:                    node.Name,
		NodeIP:                  node.IP,
		StartedUnixMS:           started.UnixMilli(),
		RebootRequestedUnixMS:   rebootRequested.UnixMilli(),
		FinishedUnixMS:          finished.UnixMilli(),
		TotalSamples:            len(samples),
		FailedSamples:           failed,
		MaxOutageMS:             maxOutage,
		OutageWindows:           windows,
		KubernetesNodeRecovered: recovered,
	}
	if !notReadyAt.IsZero() {
		report.NodeNotReadyUnixMS = notReadyAt.UnixMilli()
	}
	if !readyAt.IsZero() {
		report.NodeReadyUnixMS = readyAt.UnixMilli()
	}
	return report
}

func outageWindows(samples []sample) ([]outageWindow, int) {
	var windows []outageWindow
	var current *outageWindow
	failed := 0
	for _, s := range samples {
		if s.OK {
			if current != nil {
				current.EndUnixMS = s.TimeUnixMS
				current.DurationMS = current.EndUnixMS - current.StartUnixMS
				windows = append(windows, *current)
				current = nil
			}
			continue
		}
		failed++
		if current == nil {
			current = &outageWindow{StartUnixMS: s.TimeUnixMS}
		}
		current.FailedSamples++
	}
	if current != nil {
		current.EndUnixMS = samples[len(samples)-1].TimeUnixMS
		current.DurationMS = current.EndUnixMS - current.StartUnixMS
		windows = append(windows, *current)
	}
	return windows, failed
}

func writeReport(path string, value report) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote outage report %s\n", path)
	return nil
}

func parseStatuses(value string) ([]int, error) {
	var out []int
	for _, raw := range strings.Split(value, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			return nil, errors.New("--expected-statuses contains an empty entry")
		}
		status, err := strconv.Atoi(part)
		if err != nil {
			return nil, err
		}
		if status < 100 || status > 599 {
			return nil, fmt.Errorf("invalid HTTP status %d", status)
		}
		out = append(out, status)
	}
	return out, nil
}
