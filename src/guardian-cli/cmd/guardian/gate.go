package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const publicHTTPGateKind = "guardian.gate.public-http.v1"

type gateResult struct {
	Kind    string            `json:"kind"`
	Site    string            `json:"site"`
	Cluster string            `json:"cluster"`
	Window  string            `json:"window"`
	Passed  bool              `json:"passed"`
	Checks  []gateCheckResult `json:"checks"`
}

type gateCheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
	Error  string `json:"error,omitempty"`
}

func (r *gateResult) add(name string, passed bool, detail string, err error) {
	check := gateCheckResult{Name: name, Passed: passed, Detail: detail}
	if err != nil {
		check.Error = err.Error()
	}
	if !passed {
		r.Passed = false
	}
	r.Checks = append(r.Checks, check)
}

func (r gateResult) failedCount() int {
	var failed int
	for _, check := range r.Checks {
		if !check.Passed {
			failed++
		}
	}
	return failed
}

func runGateCmd(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("gate: %w: expected subcommand public-http", errUsage)
	}
	switch args[0] {
	case "public-http":
		return runPublicHTTPGate(args[1:])
	default:
		return fmt.Errorf("gate: %w: unknown subcommand %q", errUsage, args[0])
	}
}

func runPublicHTTPGate(args []string) error {
	fs := flag.NewFlagSet("gate public-http", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	windowArg := fs.String("window", "", "Prometheus range window for restart and 5xx checks; defaults to the public-http SLOProfile")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println(usage)
			return nil
		}
		return fmt.Errorf("gate public-http: %w: %v", errUsage, err)
	}
	site, _, err := resolveSite(fs.Args())
	if err != nil {
		return fmt.Errorf("gate public-http: %w", err)
	}
	window, err := publicHTTPGateWindow(site, *windowArg)
	if err != nil {
		return fmt.Errorf("gate public-http: %w", err)
	}
	kubectl, err := kubectlPath()
	if err != nil {
		return err
	}
	state, err := stateDir(site.Cluster.Name)
	if err != nil {
		return err
	}
	kubeconfig := filepath.Join(state, "kubeconfig")
	deps := kubernetesPublicHTTPGateDeps(kubectl, kubeconfig)
	result := evaluatePublicHTTPGate(site, window, deps)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(result); err != nil {
		return err
	}
	if !result.Passed {
		return fmt.Errorf("public-http gate failed: %d/%d checks failed", result.failedCount(), len(result.Checks))
	}
	return nil
}

type publicHTTPGateDeps struct {
	queryVM    func(string) (float64, bool, error)
	deployment func(namespace, name string) (gateDeploymentStatus, error)
	rollout    func(namespace, resource string) error
	getURL     func(string) (int, error)
}

type gateDeploymentStatus struct {
	Desired            int
	Available          int
	Updated            int
	ObservedGeneration int64
	Generation         int64
}

func (s gateDeploymentStatus) ready() bool {
	return s.Desired > 0 &&
		s.Available >= s.Desired &&
		s.Updated >= s.Desired &&
		s.ObservedGeneration >= s.Generation
}

type publicHTTPGateApp struct {
	app        string
	namespace  string
	deployment string
	metric     string
}

func evaluatePublicHTTPGate(site *Site, window time.Duration, deps publicHTTPGateDeps) gateResult {
	result := gateResult{
		Kind:    publicHTTPGateKind,
		Site:    site.Name,
		Cluster: site.Cluster.Name,
		Window:  promDuration(window),
		Passed:  true,
	}
	for _, rollout := range []environmentRollout{
		{namespace: "observability", resource: "deployment/victoria-metrics"},
		{namespace: "observability", resource: "deployment/otel-collector"},
		{namespace: "observability", resource: "deployment/vmalert"},
		{namespace: "observability", resource: "deployment/blackbox-exporter"},
		{namespace: "observability", resource: "deployment/kube-state-metrics"},
	} {
		err := deps.rollout(rollout.namespace, rollout.resource)
		result.add("rollout "+rollout.namespace+"/"+rollout.resource, err == nil, "Kubernetes rollout is complete", err)
	}
	profile := site.SLO.PublicHTTP
	if profile == nil {
		result.add("slo profile public-http", false, "environment must declare SLOProfile surface=public-http", nil)
		return result
	}
	for _, check := range []struct {
		name  string
		query string
		want  float64
	}{
		{name: "victoria-metrics self scrape", query: `count(up{job="victoria-metrics-self"} == 1)`, want: 1},
		{name: "otel collector self scrape", query: `count(up{job="otelcol-self"} == 1)`, want: 1},
		{name: "kube-state-metrics scrape", query: `count(up{job="kube-state-metrics"} == 1)`, want: 1},
	} {
		addPromEquals(&result, deps, check.name, check.query, check.want)
	}
	if signalEnabled(profile.Signals.PageAlerts) {
		addPromEquals(&result, deps, "page alerts quiet", `sum(ALERTS{alertstate="firing",severity="page"}) or vector(0)`, 0)
	}
	for _, app := range profile.Apps {
		status, err := deps.deployment(app.Namespace, app.Deployment)
		if err != nil {
			result.add("deployment "+app.Namespace+"/"+app.Deployment, false, "read deployment status", err)
			continue
		}
		result.add("deployment "+app.Namespace+"/"+app.Deployment, status.ready(), fmt.Sprintf("desired=%d available=%d updated=%d observedGeneration=%d generation=%d", status.Desired, status.Available, status.Updated, status.ObservedGeneration, status.Generation), nil)
		if signalEnabled(profile.Signals.PublicScrape) {
			addPromEquals(&result, deps, "public-http scrape "+app.Name, fmt.Sprintf("count(up{job=\"public-http\",app=%s} == 1)", promString(app.Name)), float64(status.Desired))
		}
		if signalEnabled(profile.Signals.ErrorRate) {
			addPromEquals(&result, deps, "5xx budget "+app.Name, fmt.Sprintf("sum(increase(%s{code=~\"5..\",handler!=\"GET /healthz\"}[%s])) or vector(0)", app.Metric, promDuration(window)), 0)
		}
	}
	if signalEnabled(profile.Signals.RestartDelta) {
		addPromEquals(&result, deps, "product restart delta", fmt.Sprintf("sum(increase(kube_pod_container_status_restarts_total{namespace=~\"aisucks|company\"}[%s])) or vector(0)", promDuration(window)), 0)
	}
	if signalEnabled(profile.Signals.Synthetic) {
		for _, target := range publicHTTPGateBlackboxTargets(site) {
			addPromEquals(&result, deps, "blackbox "+target, fmt.Sprintf("probe_success{job=\"blackbox\",instance=%s}", promString(target)), 1)
		}
	}
	if signalEnabled(profile.Signals.DirectHTTP) {
		for _, target := range publicHTTPGateURLs(site) {
			status, err := deps.getURL(target)
			result.add("http "+target, err == nil && status == http.StatusOK, fmt.Sprintf("status=%d", status), err)
		}
	}
	return result
}

func addPromEquals(result *gateResult, deps publicHTTPGateDeps, name, query string, want float64) {
	got, present, err := deps.queryVM(query)
	if err != nil {
		result.add(name, false, "query="+query, err)
		return
	}
	if !present {
		result.add(name, false, "query returned no series: "+query, nil)
		return
	}
	result.add(name, got == want, fmt.Sprintf("got=%s want=%s query=%s", gateFloat(got), gateFloat(want), query), nil)
}

func kubernetesPublicHTTPGateDeps(kubectl, kubeconfig string) publicHTTPGateDeps {
	return publicHTTPGateDeps{
		queryVM: func(query string) (float64, bool, error) {
			raw, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "get", "--raw", victoriaMetricsQueryPath(query))
			if err != nil {
				return 0, false, err
			}
			return parseVictoriaMetricsValue([]byte(raw))
		},
		deployment: func(namespace, name string) (gateDeploymentStatus, error) {
			raw, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", namespace, "get", "deployment/"+name, "-o", "json")
			if err != nil {
				return gateDeploymentStatus{}, err
			}
			return parseDeploymentStatus([]byte(raw))
		},
		rollout: func(namespace, resource string) error {
			_, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", namespace, "rollout", "status", resource, "--timeout=2m")
			return err
		},
		getURL: httpStatus,
	}
}

func victoriaMetricsQueryPath(query string) string {
	return "/api/v1/namespaces/observability/services/victoria-metrics:8428/proxy/api/v1/query?query=" + url.QueryEscape(query)
}

func parseVictoriaMetricsValue(raw []byte) (float64, bool, error) {
	var resp struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, false, err
	}
	if resp.Status != "success" {
		if resp.Error == "" {
			resp.Error = string(raw)
		}
		return 0, false, fmt.Errorf("victoria-metrics query failed: %s", resp.Error)
	}
	if len(resp.Data.Result) == 0 {
		return 0, false, nil
	}
	if len(resp.Data.Result) != 1 {
		return 0, false, fmt.Errorf("victoria-metrics query returned %d series, want 1", len(resp.Data.Result))
	}
	value := resp.Data.Result[0].Value
	if len(value) != 2 {
		return 0, false, fmt.Errorf("victoria-metrics value has %d elements, want 2", len(value))
	}
	rawValue, ok := value[1].(string)
	if !ok {
		return 0, false, fmt.Errorf("victoria-metrics value is %T, want string", value[1])
	}
	parsed, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse victoria-metrics value %q: %w", rawValue, err)
	}
	return parsed, true, nil
}

func parseDeploymentStatus(raw []byte) (gateDeploymentStatus, error) {
	var deploy struct {
		Metadata struct {
			Generation int64 `json:"generation"`
		} `json:"metadata"`
		Spec struct {
			Replicas *int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			AvailableReplicas  int   `json:"availableReplicas"`
			UpdatedReplicas    int   `json:"updatedReplicas"`
			ObservedGeneration int64 `json:"observedGeneration"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &deploy); err != nil {
		return gateDeploymentStatus{}, err
	}
	desired := 1
	if deploy.Spec.Replicas != nil {
		desired = *deploy.Spec.Replicas
	}
	return gateDeploymentStatus{
		Desired:            desired,
		Available:          deploy.Status.AvailableReplicas,
		Updated:            deploy.Status.UpdatedReplicas,
		ObservedGeneration: deploy.Status.ObservedGeneration,
		Generation:         deploy.Metadata.Generation,
	}, nil
}

func httpStatus(target string) (int, error) {
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return resp.StatusCode, nil
}

func publicHTTPGateBlackboxTargets(site *Site) []string {
	var targets []string
	targets = append(targets, site.Aisucks.Watch...)
	targets = append(targets, site.Aisucks.WatchPages...)
	targets = append(targets, site.Company.ProbeURLs...)
	return uniqueNonEmpty(targets)
}

func publicHTTPGateURLs(site *Site) []string {
	var targets []string
	add := func(domain, route string) {
		domain = strings.TrimSpace(domain)
		route = strings.TrimSpace(route)
		if domain == "" || route == "" {
			return
		}
		if !strings.HasPrefix(route, "/") {
			route = "/" + route
		}
		targets = append(targets, "https://"+domain+route)
	}
	add(site.Aisucks.Domain, "/healthz")
	add(site.Aisucks.Domain, "/")
	add(site.Company.Domain, "/healthz")
	for _, route := range site.Company.Routes {
		add(site.Company.Domain, route)
	}
	return uniqueNonEmpty(targets)
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func promString(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + replacer.Replace(value) + `"`
}

func promDuration(d time.Duration) string {
	seconds := int64(d.Round(time.Second) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(seconds, 10) + "s"
}

func gateFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func publicHTTPGateWindow(site *Site, override string) (time.Duration, error) {
	raw := override
	if raw == "" && site.SLO.PublicHTTP != nil {
		raw = site.SLO.PublicHTTP.Window
	}
	if raw == "" {
		raw = "15m"
	}
	window, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("--window %q: %w", raw, err)
	}
	if window < time.Second {
		return 0, fmt.Errorf("%w: --window must be at least 1s", errUsage)
	}
	return window, nil
}
