package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

// The collector owns the refresh loop: query the site-local VictoriaMetrics
// for the rollout state of every workload on THIS site, assemble the Document
// (one isDeploying boolean per workload, grouped by namespace), render once,
// publish the immutable snapshot. Handlers only ever serve cached bytes.
//
// Cross-site isolation is the design: this only ever reads its own site's
// kube-state-metrics — never a sibling's control plane. The dev→gamma→prod
// promotion view is built by reading each site's OWN public page in turn (the
// one cross-site act the charter permits — observing a public surface, the
// same principle as a blackbox probe), not by one page reaching across.

const refreshSeconds = 10

// isDeploying per workload kind, as a 0/1 vector keyed by namespace + the
// kind's name label. "Deploying" means NOT fully rolled out and available on
// the current spec generation (kubectl-rollout-status semantics): a wedged or
// crash-looping rollout stays 1 until it genuinely converges, so the boolean
// goes 1→0 only when the new version is actually up. Source is
// kube-state-metrics, so the floor on granularity is the scrape interval — a
// fast roll that completes between two scrapes can read 0 throughout; a roll
// that is STUCK (the case worth seeing) reads 1 for as long as it is stuck.
const (
	qDeployments = `clamp_max(` +
		`(kube_deployment_spec_replicas - kube_deployment_status_replicas_updated > bool 0)` +
		` + (kube_deployment_spec_replicas - kube_deployment_status_replicas_available > bool 0)` +
		` + (kube_deployment_status_replicas - kube_deployment_status_replicas_updated > bool 0)` +
		` + (kube_deployment_metadata_generation - kube_deployment_status_observed_generation > bool 0), 1)`
	qStatefulSets = `clamp_max(` +
		`(kube_statefulset_replicas - kube_statefulset_status_replicas_updated > bool 0)` +
		` + (kube_statefulset_replicas - kube_statefulset_status_replicas_ready > bool 0)` +
		` + (kube_statefulset_metadata_generation - kube_statefulset_status_observed_generation > bool 0), 1)`
	qDaemonSets = `clamp_max(` +
		`(kube_daemonset_status_desired_number_scheduled - kube_daemonset_status_updated_number_scheduled > bool 0)` +
		` + (kube_daemonset_status_desired_number_scheduled - kube_daemonset_status_number_available > bool 0)` +
		` + (kube_daemonset_status_number_unavailable > bool 0)` +
		` + (kube_daemonset_metadata_generation - kube_daemonset_status_observed_generation > bool 0), 1)`
)

// kinds pairs each query with the kube-state-metrics label that names the
// workload (deployment | statefulset | daemonset).
var kinds = []struct{ query, label string }{
	{qDeployments, "deployment"},
	{qStatefulSets, "statefulset"},
	{qDaemonSets, "daemonset"},
}

type sample struct {
	metric map[string]string
	value  float64
}

type vmClient struct {
	base string
	hc   *http.Client
}

// query runs one instant query against /api/v1/query and returns the vector.
func (c *vmClient) query(ctx context.Context, expr string) ([]sample, error) {
	u := c.base + "/api/v1/query?query=" + url.QueryEscape(expr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("victoriametrics: status %d", resp.StatusCode)
	}
	var vr struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &vr); err != nil {
		return nil, err
	}
	if vr.Status != "success" {
		return nil, fmt.Errorf("victoriametrics: %s", vr.Error)
	}
	out := make([]sample, 0, len(vr.Data.Result))
	for _, r := range vr.Data.Result {
		s, ok := r.Value[1].(string)
		if !ok {
			continue
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		out = append(out, sample{metric: r.Metric, value: f})
	}
	return out, nil
}

type collector struct {
	vm   *vmClient
	site string
}

func newCollector(vmURL, site string) *collector {
	return &collector{
		vm:   &vmClient{base: vmURL, hc: &http.Client{Timeout: 10 * time.Second}},
		site: site,
	}
}

// loop renders immediately, then on every tick, publishing each snapshot.
func (c *collector) loop(ctx context.Context, publish func(*snapshot)) {
	tick := func() {
		sn, err := newSnapshot(c.document(ctx))
		if err != nil {
			logger.Error("render snapshot", "error", err)
			return
		}
		publish(sn)
	}
	tick()
	t := time.NewTicker(refreshSeconds * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// document queries this site's workload rollout state and assembles the page:
// one [namespace] table per namespace, one `workload = bool` per workload.
func (c *collector) document(ctx context.Context) Document {
	byNS := map[string]map[string]bool{} // namespace -> workload -> isDeploying
	var queryErr error
	for _, k := range kinds {
		samples, err := c.vm.query(ctx, k.query)
		if err != nil {
			queryErr = err
			continue
		}
		for _, s := range samples {
			ns, name := s.metric["namespace"], s.metric[k.label]
			if ns == "" || name == "" {
				continue
			}
			if byNS[ns] == nil {
				byNS[ns] = map[string]bool{}
			}
			byNS[ns][name] = s.value >= 0.5
		}
	}

	doc := Document{Header: []string{
		"GUARDIAN INTELLIGENCE — fleet status",
		"site: " + c.site,
		"generated: " + time.Now().UTC().Format(time.RFC3339),
		"isDeploying per workload (true = a rollout is in progress or wedged).",
		"this site only — each site serves its own page (cross-site isolation).",
	}}

	if len(byNS) == 0 {
		// Every query failed or returned nothing: say so, never invent green.
		reason := "VictoriaMetrics returned no workload metrics"
		if queryErr != nil {
			reason = queryErr.Error()
		}
		doc.Sections = []Section{{Name: "status", Entries: []Entry{
			{Key: "deploy_state", Value: "unknown", Comment: reason},
		}}}
		return doc
	}

	namespaces := make([]string, 0, len(byNS))
	for ns := range byNS {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	for _, ns := range namespaces {
		names := make([]string, 0, len(byNS[ns]))
		for n := range byNS[ns] {
			names = append(names, n)
		}
		sort.Strings(names)
		sec := Section{Name: ns}
		for _, n := range names {
			sec.Entries = append(sec.Entries, Entry{Key: n, Value: byNS[ns][n]})
		}
		doc.Sections = append(doc.Sections, sec)
	}
	return doc
}
