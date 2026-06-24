package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"gopkg.in/yaml.v3"
)

const (
	defaultDoggoRunfile        = "+http_archive+doggo_linux_amd64/doggo"
	defaultK6Runfile           = "+http_archive+k6_linux_amd64/k6"
	defaultScriptRunfile       = "_main/src/infrastructure/load/edge-health.js"
	defaultDNSEndpointsRunfile = "_main/src/infrastructure/base/dns/records/dnsendpoints.yaml"
	defaultHTTPTargetsRunfile  = "_main/src/infrastructure/edge/http-targets.file_sd.yaml"
)

type config struct {
	Doggo                  string
	K6                     string
	Script                 string
	DNSEndpoints           string
	HTTPTargets            string
	Domain                 string
	DNSResolvers           string
	DNSTimeout             string
	DNSSamples             int
	DNSSampleInterval      time.Duration
	DNSMinSuccessRatio     float64
	HTTPVUs                string
	HTTPIterations         int
	HTTPMaxDuration        string
	HTTPRequestTimeout     string
	HTTPSleepSeconds       string
	K6ExpectedStatusCutoff string
	OriginChecks           bool
	WildcardProbeLabel     string
}

type dnsTarget struct {
	DNSName        string
	QueryName      string
	RecordType     string
	ExpectedValues []string
	Source         string
}

type httpTarget struct {
	URL              string
	Host             string
	Surface          string
	Stage            string
	Name             string
	ExpectedStatuses []int
	ExpectedIPs      []string
	Source           string
}

type fileSDGroup struct {
	Targets []string          `yaml:"targets"`
	Labels  map[string]string `yaml:"labels"`
}

type dnsEndpointDoc struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Endpoints []struct {
			DNSName    string   `yaml:"dnsName"`
			RecordType string   `yaml:"recordType"`
			Targets    []string `yaml:"targets"`
		} `yaml:"endpoints"`
	} `yaml:"spec"`
}

type doggoResponse struct {
	Responses []struct {
		Answers []struct {
			Type       string `json:"type"`
			Address    string `json:"address"`
			Target     string `json:"target"`
			CNAME      string `json:"cname"`
			NS         string `json:"ns"`
			Exchange   string `json:"exchange"`
			Status     string `json:"status"`
			RTT        string `json:"rtt"`
			Nameserver string `json:"nameserver"`
		} `json:"answers"`
	} `json:"responses"`
}

type dnsObservation struct {
	TargetName string
	QueryName  string
	RecordType string
	Resolver   string
	Sample     int
	Values     []string
	Expected   []string
	Matched    bool
	Err        error
	Raw        string
}

type k6Target struct {
	URL              string `json:"url"`
	Name             string `json:"name"`
	Surface          string `json:"surface"`
	Stage            string `json:"stage"`
	ExpectedStatuses []int  `json:"expected_statuses"`
}

func main() {
	if len(os.Args) != 1 {
		exitIfErr(fmt.Errorf("edge_health is self-contained and takes no arguments; run aspect infra edge-health"))
	}
	cfg, err := defaultConfig()
	exitIfErr(err)
	exitIfErr(validateConfig(cfg))
	exitIfErr(run(context.Background(), cfg))
}

func defaultConfig() (config, error) {
	doggo, err := runfile(defaultDoggoRunfile)
	if err != nil {
		return config{}, err
	}
	k6, err := runfile(defaultK6Runfile)
	if err != nil {
		return config{}, err
	}
	script, err := runfile(defaultScriptRunfile)
	if err != nil {
		return config{}, err
	}
	dnsEndpoints, err := runfile(defaultDNSEndpointsRunfile)
	if err != nil {
		return config{}, err
	}
	httpTargets, err := runfile(defaultHTTPTargetsRunfile)
	if err != nil {
		return config{}, err
	}
	return config{
		Doggo:                  doggo,
		K6:                     k6,
		Script:                 script,
		DNSEndpoints:           dnsEndpoints,
		HTTPTargets:            httpTargets,
		Domain:                 "guardianintelligence.org",
		DNSResolvers:           "1.1.1.1,8.8.8.8,9.9.9.9",
		DNSTimeout:             "5s",
		DNSSamples:             5,
		DNSSampleInterval:      2 * time.Second,
		DNSMinSuccessRatio:     0.8,
		HTTPVUs:                "1",
		HTTPIterations:         2,
		HTTPMaxDuration:        "2m",
		HTTPRequestTimeout:     "10s",
		HTTPSleepSeconds:       "1",
		K6ExpectedStatusCutoff: "rate>0.99",
		OriginChecks:           true,
		WildcardProbeLabel:     "edge-health-wildcard",
	}, nil
}

func runfile(path string) (string, error) {
	resolved, err := runfiles.Rlocation(path)
	if err != nil {
		return "", fmt.Errorf("resolve runfile %s: %w", path, err)
	}
	return resolved, nil
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg config) error {
	if cfg.Doggo == "" {
		return errors.New("doggo runfile is required")
	}
	if cfg.K6 == "" {
		return errors.New("k6 runfile is required")
	}
	if cfg.Script == "" {
		return errors.New("k6 script runfile is required")
	}
	if strings.TrimSpace(cfg.Domain) == "" {
		return errors.New("edge domain is required")
	}
	if cfg.DNSSamples <= 0 {
		return errors.New("DNS samples must be positive")
	}
	if cfg.DNSSampleInterval < 0 {
		return errors.New("DNS sample interval must be non-negative")
	}
	if cfg.DNSMinSuccessRatio < 0 || cfg.DNSMinSuccessRatio > 1 {
		return errors.New("DNS minimum success ratio must be in [0,1]")
	}
	if _, err := strconv.Atoi(cfg.HTTPVUs); err != nil {
		return fmt.Errorf("HTTP VUs must be an integer: %w", err)
	}
	if cfg.HTTPIterations <= 0 {
		return errors.New("HTTP iterations must be positive")
	}
	if _, err := strconv.ParseFloat(cfg.HTTPSleepSeconds, 64); err != nil {
		return fmt.Errorf("HTTP sleep seconds must be numeric: %w", err)
	}
	if _, err := splitNonEmptyComma(cfg.DNSEndpoints); err != nil {
		return fmt.Errorf("DNS endpoint runfiles: %w", err)
	}
	if _, err := splitNonEmptyComma(cfg.HTTPTargets); err != nil {
		return fmt.Errorf("HTTP target runfiles: %w", err)
	}
	if _, err := splitNonEmptyComma(cfg.DNSResolvers); err != nil {
		return fmt.Errorf("DNS resolvers: %w", err)
	}
	return nil
}

func run(ctx context.Context, cfg config) error {
	dnsPaths, _ := splitNonEmptyComma(cfg.DNSEndpoints)
	httpPaths, _ := splitNonEmptyComma(cfg.HTTPTargets)
	resolvers, _ := splitNonEmptyComma(cfg.DNSResolvers)

	dnsTargets, err := loadDNSTargets(dnsPaths, cfg.WildcardProbeLabel)
	if err != nil {
		return err
	}
	if len(dnsTargets) == 0 {
		return errors.New("no DNS targets discovered from DNSEndpoint manifests")
	}
	httpTargets, err := loadHTTPTargets(httpPaths, dnsTargets, cfg.Domain)
	if err != nil {
		return err
	}

	fmt.Printf("guardian edge health\n")
	fmt.Printf("dnsTargets=%d httpTargets=%d resolvers=%s dnsSamples=%d dnsMinSuccessRatio=%.2f originChecks=%t\n",
		len(dnsTargets),
		len(httpTargets),
		strings.Join(resolvers, ","),
		cfg.DNSSamples,
		cfg.DNSMinSuccessRatio,
		cfg.OriginChecks,
	)

	dnsErr := runDNS(ctx, cfg, dnsTargets, resolvers)
	httpErr := runHTTP(ctx, cfg, httpTargets)
	return errors.Join(dnsErr, httpErr)
}

func loadDNSTargets(paths []string, wildcardLabel string) ([]dnsTarget, error) {
	merged := map[string]dnsTarget{}
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open DNS endpoints %s: %w", path, err)
		}
		decoder := yaml.NewDecoder(file)
		for {
			var doc dnsEndpointDoc
			err := decoder.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				_ = file.Close()
				return nil, fmt.Errorf("decode DNS endpoints %s: %w", path, err)
			}
			if doc.Kind != "DNSEndpoint" {
				continue
			}
			for _, endpoint := range doc.Spec.Endpoints {
				dnsName := normalizeDNSName(endpoint.DNSName)
				recordType := strings.ToUpper(strings.TrimSpace(endpoint.RecordType))
				if recordType == "" {
					recordType = "A"
				}
				if dnsName == "" {
					_ = file.Close()
					return nil, fmt.Errorf("%s has DNSEndpoint with empty dnsName", path)
				}
				if len(endpoint.Targets) == 0 {
					_ = file.Close()
					return nil, fmt.Errorf("%s DNSEndpoint %s/%s has no targets", path, dnsName, recordType)
				}
				target := dnsTarget{
					DNSName:        dnsName,
					QueryName:      queryNameForDNSName(dnsName, wildcardLabel),
					RecordType:     recordType,
					ExpectedValues: normalizeRecordValues(endpoint.Targets),
					Source:         path,
				}
				key := target.DNSName + "\x00" + target.RecordType
				if existing, ok := merged[key]; ok {
					existing.ExpectedValues = sortedUnique(append(existing.ExpectedValues, target.ExpectedValues...))
					merged[key] = existing
					continue
				}
				merged[key] = target
			}
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close DNS endpoints %s: %w", path, err)
		}
	}
	out := make([]dnsTarget, 0, len(merged))
	for _, target := range merged {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DNSName == out[j].DNSName {
			return out[i].RecordType < out[j].RecordType
		}
		return out[i].DNSName < out[j].DNSName
	})
	return out, nil
}

func loadHTTPTargets(paths []string, dnsTargets []dnsTarget, domain string) ([]httpTarget, error) {
	var out []httpTarget
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open HTTP targets %s: %w", path, err)
		}
		var groups []fileSDGroup
		if err := yaml.NewDecoder(file).Decode(&groups); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("decode HTTP targets %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close HTTP targets %s: %w", path, err)
		}
		for groupIndex, group := range groups {
			expectedStatuses, err := parseExpectedStatuses(group.Labels["guardian_expected_statuses"])
			if err != nil {
				return nil, fmt.Errorf("%s group %d: %w", path, groupIndex, err)
			}
			stage := defaultString(group.Labels["guardian_stage"], "root")
			surface := defaultString(group.Labels["guardian_surface"], "edge")
			for _, rawTarget := range group.Targets {
				parsed, err := url.Parse(strings.TrimSpace(rawTarget))
				if err != nil || parsed.Scheme == "" || parsed.Host == "" {
					return nil, fmt.Errorf("%s target %q must be an absolute HTTP URL", path, rawTarget)
				}
				if parsed.Scheme != "https" && parsed.Scheme != "http" {
					return nil, fmt.Errorf("%s target %q must use http or https", path, rawTarget)
				}
				host := normalizeDNSName(parsed.Hostname())
				if !isUnderDomain(host, domain) {
					return nil, fmt.Errorf("%s target %q host is outside %s", path, rawTarget, domain)
				}
				expectedIPs := expectedValuesForHost(host, dnsTargets)
				if len(expectedIPs) == 0 {
					return nil, fmt.Errorf("%s target %q has no matching DNSEndpoint", path, rawTarget)
				}
				out = append(out, httpTarget{
					URL:              parsed.String(),
					Host:             host,
					Surface:          surface,
					Stage:            stage,
					Name:             requestName(stage, surface, host),
					ExpectedStatuses: expectedStatuses,
					ExpectedIPs:      expectedIPs,
					Source:           path,
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].URL < out[j].URL
	})
	return out, nil
}

func runDNS(ctx context.Context, cfg config, targets []dnsTarget, resolvers []string) error {
	fmt.Printf("\n## DNS probes\n")
	observations := make([]dnsObservation, 0, len(targets)*len(resolvers)*cfg.DNSSamples)
	for sample := 1; sample <= cfg.DNSSamples; sample++ {
		for _, target := range targets {
			for _, resolver := range resolvers {
				observations = append(observations, runDoggo(ctx, cfg, target, resolver, sample))
			}
		}
		if sample < cfg.DNSSamples && cfg.DNSSampleInterval > 0 {
			time.Sleep(cfg.DNSSampleInterval)
		}
	}

	var failures []string
	for _, target := range targets {
		matched := 0
		total := 0
		var bad []dnsObservation
		for _, observation := range observations {
			if observation.TargetName != target.DNSName || observation.RecordType != target.RecordType {
				continue
			}
			total++
			if observation.Matched {
				matched++
			} else if len(bad) < 5 {
				bad = append(bad, observation)
			}
		}
		ratio := float64(matched) / float64(total)
		passed := ratio >= cfg.DNSMinSuccessRatio
		fmt.Printf("%s %s query=%s expected=%s matched=%d/%d ratio=%.2f pass=%t\n",
			target.DNSName,
			target.RecordType,
			target.QueryName,
			strings.Join(target.ExpectedValues, ","),
			matched,
			total,
			ratio,
			passed,
		)
		for _, observation := range bad {
			errText := ""
			if observation.Err != nil {
				errText = " err=" + observation.Err.Error()
			}
			fmt.Printf("  sample=%d resolver=%s got=%s%s\n",
				observation.Sample,
				observation.Resolver,
				strings.Join(observation.Values, ","),
				errText,
			)
		}
		if !passed {
			failures = append(failures, fmt.Sprintf("%s %s matched %d/%d observations", target.DNSName, target.RecordType, matched, total))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("DNS confidence below threshold: %s", strings.Join(failures, "; "))
	}
	return nil
}

func runDoggo(ctx context.Context, cfg config, target dnsTarget, resolver string, sample int) dnsObservation {
	args := []string{
		"-J",
		"-q", target.QueryName,
		"-t", target.RecordType,
		"-n", resolver,
		"--timeout", cfg.DNSTimeout,
	}
	cmd := exec.CommandContext(ctx, cfg.Doggo, args...)
	output, err := cmd.CombinedOutput()
	observation := dnsObservation{
		TargetName: target.DNSName,
		QueryName:  target.QueryName,
		RecordType: target.RecordType,
		Resolver:   resolver,
		Sample:     sample,
		Expected:   target.ExpectedValues,
		Err:        err,
		Raw:        string(output),
	}
	if err != nil {
		return observation
	}

	var parsed doggoResponse
	if err := json.Unmarshal(output, &parsed); err != nil {
		observation.Err = fmt.Errorf("parse doggo JSON: %w", err)
		return observation
	}
	values := make([]string, 0)
	for _, response := range parsed.Responses {
		for _, answer := range response.Answers {
			if !strings.EqualFold(answer.Type, target.RecordType) {
				continue
			}
			value := recordValue(answer)
			if value != "" {
				values = append(values, value)
			}
		}
	}
	observation.Values = normalizeRecordValues(values)
	observation.Matched = sameStringSet(observation.Values, observation.Expected)
	return observation
}

func runHTTP(ctx context.Context, cfg config, targets []httpTarget) error {
	if len(targets) == 0 {
		fmt.Printf("\n## HTTP probes\nno HTTP targets configured\n")
		return nil
	}
	fmt.Printf("\n## HTTP probes\n")
	for _, target := range targets {
		fmt.Printf("%s expectedStatuses=%s expectedIPs=%s source=%s\n",
			target.URL,
			intsString(target.ExpectedStatuses),
			strings.Join(target.ExpectedIPs, ","),
			target.Source,
		)
	}

	var errs []error
	if err := runK6(ctx, cfg, "public-dns", targets, nil); err != nil {
		errs = append(errs, err)
	}
	if cfg.OriginChecks {
		for origin, originTargets := range targetsByOrigin(targets) {
			hosts := map[string]string{}
			for _, target := range originTargets {
				hosts[target.Host] = origin
			}
			if err := runK6(ctx, cfg, origin, originTargets, hosts); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func runK6(ctx context.Context, cfg config, origin string, targets []httpTarget, hosts map[string]string) error {
	payload := make([]k6Target, 0, len(targets))
	for _, target := range targets {
		payload = append(payload, k6Target{
			URL:              target.URL,
			Name:             target.Name,
			Surface:          target.Surface,
			Stage:            target.Stage,
			ExpectedStatuses: target.ExpectedStatuses,
		})
	}
	targetJSON, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	args := []string{
		"run",
		"--summary-trend-stats",
		"avg,min,med,p(95),p(99),max",
		cfg.Script,
	}
	fmt.Printf("\n### k6 %s\n", origin)
	cmd := exec.CommandContext(ctx, cfg.K6, args...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env,
		"EDGE_TARGETS_JSON="+string(targetJSON),
		"EDGE_K6_ORIGIN="+origin,
		"EDGE_K6_VUS="+cfg.HTTPVUs,
		"EDGE_K6_ITERATIONS="+strconv.Itoa(cfg.HTTPIterations),
		"EDGE_K6_MIN_REQUESTS="+strconv.Itoa(len(targets)*cfg.HTTPIterations),
		"EDGE_K6_MAX_DURATION="+cfg.HTTPMaxDuration,
		"EDGE_K6_REQUEST_TIMEOUT="+cfg.HTTPRequestTimeout,
		"EDGE_K6_SLEEP_SECONDS="+cfg.HTTPSleepSeconds,
		"EDGE_K6_EXPECTED_STATUS_THRESHOLD="+cfg.K6ExpectedStatusCutoff,
	)
	if len(hosts) > 0 {
		cmd.Env = append(cmd.Env, "EDGE_K6_HOSTS="+formatHostOverrides(hosts))
		fmt.Printf("hostOverrides=%s\n", formatHostOverrides(hosts))
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	fmt.Print(output.String())
	if err != nil {
		return fmt.Errorf("k6 %s: %w", origin, err)
	}
	return nil
}

func targetsByOrigin(targets []httpTarget) map[string][]httpTarget {
	out := map[string][]httpTarget{}
	for _, target := range targets {
		for _, ip := range target.ExpectedIPs {
			out[ip] = append(out[ip], target)
		}
	}
	origins := make([]string, 0, len(out))
	for origin := range out {
		origins = append(origins, origin)
	}
	sort.Strings(origins)
	sorted := map[string][]httpTarget{}
	for _, origin := range origins {
		sorted[origin] = out[origin]
	}
	return sorted
}

func expectedValuesForHost(host string, targets []dnsTarget) []string {
	for _, target := range targets {
		if target.RecordType == "A" && target.DNSName == host {
			return target.ExpectedValues
		}
	}
	for _, target := range targets {
		if target.RecordType == "A" && wildcardMatches(target.DNSName, host) {
			return target.ExpectedValues
		}
	}
	return nil
}

func queryNameForDNSName(dnsName, wildcardLabel string) string {
	dnsName = normalizeDNSName(dnsName)
	if strings.HasPrefix(dnsName, "*.") {
		return normalizeDNSName(wildcardLabel + "." + strings.TrimPrefix(dnsName, "*."))
	}
	return dnsName
}

func wildcardMatches(pattern, host string) bool {
	pattern = normalizeDNSName(pattern)
	host = normalizeDNSName(host)
	if !strings.HasPrefix(pattern, "*.") {
		return false
	}
	suffix := strings.TrimPrefix(pattern, "*.")
	return host != suffix && strings.HasSuffix(host, "."+suffix)
}

func isUnderDomain(host, domain string) bool {
	host = normalizeDNSName(host)
	domain = normalizeDNSName(domain)
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func parseExpectedStatuses(value string) ([]int, error) {
	if strings.TrimSpace(value) == "" {
		return []int{200}, nil
	}
	var out []int
	for _, raw := range strings.Split(value, ",") {
		part := strings.TrimSpace(raw)
		status, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("guardian_expected_statuses contains non-integer %q", part)
		}
		if status < 100 || status > 599 {
			return nil, fmt.Errorf("guardian_expected_statuses contains invalid HTTP status %d", status)
		}
		out = append(out, status)
	}
	sort.Ints(out)
	return uniqueInts(out), nil
}

func recordValue(answer struct {
	Type       string `json:"type"`
	Address    string `json:"address"`
	Target     string `json:"target"`
	CNAME      string `json:"cname"`
	NS         string `json:"ns"`
	Exchange   string `json:"exchange"`
	Status     string `json:"status"`
	RTT        string `json:"rtt"`
	Nameserver string `json:"nameserver"`
}) string {
	for _, value := range []string{answer.Address, answer.Target, answer.CNAME, answer.NS, answer.Exchange} {
		if strings.TrimSpace(value) != "" {
			return normalizeRecordValue(value)
		}
	}
	return ""
}

func normalizeRecordValues(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		normalized := normalizeRecordValue(value)
		if normalized != "" {
			out = append(out, normalized)
		}
	}
	return sortedUnique(out)
}

func normalizeRecordValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(value, ".")
}

func normalizeDNSName(value string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
}

func sameStringSet(left, right []string) bool {
	left = sortedUnique(left)
	right = sortedUnique(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func uniqueInts(values []int) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func splitNonEmptyComma(value string) ([]string, error) {
	var out []string
	for _, raw := range strings.Split(value, ",") {
		part := strings.TrimSpace(raw)
		if part == "" {
			return nil, errors.New("contains an empty comma-separated entry")
		}
		out = append(out, part)
	}
	return out, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func requestName(stage, surface, host string) string {
	return "guardian-edge-" + sanitizeLabel(stage) + "-" + sanitizeLabel(surface) + "-" + sanitizeLabel(host)
}

func sanitizeLabel(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func intsString(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}

func formatHostOverrides(hosts map[string]string) string {
	keys := make([]string, 0, len(hosts))
	for host := range hosts {
		keys = append(keys, host)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, host := range keys {
		parts = append(parts, host+"="+hosts[host])
	}
	return strings.Join(parts, ",")
}
