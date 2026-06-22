package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func stableHash(input any) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func gatusConfig(site *Host) string {
	var b strings.Builder
	alert := site.Aisucks.NtfyTopic != ""
	if alert {
		fmt.Fprintf(&b, `alerting:
  ntfy:
    url: "https://ntfy.sh"
    topic: "%s"
    priority: 4
    default-alert:
      enabled: true
      failure-threshold: 3
      success-threshold: 2
      send-on-resolved: true
`, site.Aisucks.NtfyTopic)
	}
	b.WriteString("endpoints:\n")
	base := "http://" + site.Node.Address
	if site.Aisucks.Domain != "" {
		base = "https://" + site.Aisucks.Domain
	}
	writeGatusEndpoint(&b, "aisucks-healthz ("+site.Cluster.Name+")", base+"/healthz", []string{"[STATUS] == 200"}, alert)
	writeGatusEndpoint(&b, "aisucks-page ("+site.Cluster.Name+")", base+"/", []string{
		"[STATUS] == 200",
		"[BODY] == pat(*never be sold*)",
	}, alert)
	for i, target := range site.Aisucks.Watch {
		writeGatusEndpoint(&b, fmt.Sprintf("watch-%d", i), target, []string{"[STATUS] == 200"}, alert)
	}
	for i, target := range site.Aisucks.WatchPages {
		writeGatusEndpoint(&b, fmt.Sprintf("watch-page-%d", i), target, []string{
			"[STATUS] == 200",
			"[BODY] == pat(*never be sold*)",
		}, alert)
	}
	return b.String()
}

func writeGatusEndpoint(b *strings.Builder, name, url string, conditions []string, alert bool) {
	fmt.Fprintf(b, `  - name: %s
    url: "%s"
    interval: 30s
    conditions:
`, name, url)
	for _, condition := range conditions {
		fmt.Fprintf(b, "      - %q\n", condition)
	}
	if alert {
		b.WriteString(`    alerts:
      - type: ntfy
`)
	}
}

func otelCollectorConfig(site *Host) string {
	var b strings.Builder
	if site.Clickhouse.Enabled {
		b.WriteString(`extensions:
  file_storage:
    directory: /var/lib/otel-collector
`)
	}
	fmt.Fprintf(&b, `receivers:
  prometheus:
    config:
      global:
        scrape_interval: 15s
        external_labels:
          cluster: "%s"
      scrape_configs:
        - job_name: public-http
          kubernetes_sd_configs:
            - role: pod
          relabel_configs:
            - source_labels: [__meta_kubernetes_pod_label_platform_guardian_dev_metrics_scrape]
              regex: "true"
              action: keep
            - source_labels: [__meta_kubernetes_pod_phase]
              regex: Running
              action: keep
            - source_labels:
                - __meta_kubernetes_pod_ip
                - __meta_kubernetes_pod_label_platform_guardian_dev_metrics_port
              separator: ":"
              regex: (.+):(.+)
              target_label: __address__
              replacement: $${1}:$${2}
            - source_labels: [__meta_kubernetes_namespace]
              target_label: namespace
            - source_labels: [__meta_kubernetes_pod_label_app]
              target_label: app
            - source_labels: [__meta_kubernetes_pod_label_platform_guardian_dev_slo_surface]
              target_label: slo_surface
            - source_labels: [__meta_kubernetes_pod_name]
              target_label: instance
        - job_name: hubble
          static_configs:
            - targets: ["127.0.0.1:9965"]
        - job_name: kube-state-metrics
          static_configs:
            - targets: ["kube-state-metrics.observability.svc:8080"]
`, site.Cluster.Name)
	if targets := otelBlackboxTargets(site); len(targets) > 0 {
		b.WriteString(`        - job_name: blackbox
          metrics_path: /probe
          params:
            module: [http_2xx]
          static_configs:
            - targets:
`)
		for _, target := range targets {
			fmt.Fprintf(&b, "                - %q\n", target)
		}
		b.WriteString(`          relabel_configs:
            - source_labels: [__address__]
              target_label: __param_target
            - source_labels: [__param_target]
              target_label: instance
            - target_label: __address__
              replacement: blackbox-exporter.observability.svc:9115
`)
	}
	b.WriteString(`        - job_name: cadvisor
          scheme: https
          metrics_path: /metrics/cadvisor
          bearer_token_file: /var/run/secrets/kubernetes.io/serviceaccount/token
          tls_config:
            insecure_skip_verify: true
          static_configs:
            - targets: ["127.0.0.1:10250"]
        - job_name: otelcol-self
          static_configs:
            - targets: ["127.0.0.1:8888"]
        - job_name: victoria-metrics-self
          static_configs:
            - targets: ["victoria-metrics.observability.svc:8428"]
`)
	if site.Clickhouse.Enabled {
		b.WriteString(`  filelog:
    include: [/var/log/pods/*/*/*.log]
    exclude: [/var/log/pods/observability_otel-collector*/*/*.log]
    storage: file_storage
    include_file_path: true
    operators:
      - type: container
  k8sobjects:
    auth_type: serviceAccount
    objects:
      - name: events
        mode: watch
`)
	}
	b.WriteString(`processors:
  memory_limiter:
    check_interval: 1s
`)
	if site.Clickhouse.Enabled {
		b.WriteString(`    limit_mib: 384
    spike_limit_mib: 96
`)
	} else {
		b.WriteString(`    limit_mib: 256
    spike_limit_mib: 64
`)
	}
	b.WriteString("  batch: {}\n")
	if site.Clickhouse.Enabled {
		fmt.Fprintf(&b, `  resource/site:
    attributes:
      - key: k8s.cluster.name
        value: "%s"
        action: upsert
      - key: k8s.node.name
        value: "%s"
        action: upsert
`, site.Cluster.Name, site.Node.Hostname)
	}
	b.WriteString(`exporters:
  prometheusremotewrite:
    endpoint: http://victoria-metrics.observability.svc:8428/api/v1/write
`)
	if site.Clickhouse.Enabled {
		b.WriteString(`  clickhouse:
    endpoint: tcp://clickhouse.observability.svc:9000
    database: otel
    username: default
    password: ${env:CLICKHOUSE_ADMIN_PASSWORD:-}
    create_schema: false
    timeout: 10s
`)
	}
	b.WriteString("service:\n")
	if site.Clickhouse.Enabled {
		b.WriteString("  extensions: [file_storage]\n")
	}
	fmt.Fprintf(&b, `  telemetry:
    resource:
      service.name: otelcol-self
      service.instance.id: "%s"
    metrics:
      readers:
        - pull:
            exporter:
              prometheus:
                host: "127.0.0.1"
                port: 8888
  pipelines:
    metrics:
      receivers: [prometheus]
      processors: [memory_limiter, batch]
      exporters: [prometheusremotewrite]
`, site.Cluster.Name)
	if site.Clickhouse.Enabled {
		b.WriteString(`    logs:
      receivers: [filelog, k8sobjects]
      processors: [memory_limiter, resource/site, batch]
      exporters: [clickhouse]
`)
	}
	return b.String()
}

func otelBlackboxTargets(site *Host) []string {
	targets := make([]string, 0, len(site.Aisucks.Watch)+len(site.Aisucks.WatchPages)+len(site.Status.Domains))
	targets = append(targets, site.Aisucks.Watch...)
	targets = append(targets, site.Aisucks.WatchPages...)
	if site.Status.Monitor {
		for _, domain := range site.Status.Domains {
			targets = append(targets, "https://"+domain+"/healthz")
		}
	}
	return targets
}

func otelCollectorLedgerPatches() []kustomizePatch {
	return []kustomizePatch{{
		kind: "ClusterRole",
		name: "otel-collector",
		op:   "add",
		path: "/rules/-",
		value: map[string]any{
			"apiGroups": []string{"", "events.k8s.io"},
			"resources": []string{"events"},
			"verbs":     []string{"get", "list", "watch"},
		},
	}, {
		kind:  "Deployment",
		name:  "otel-collector",
		op:    "add",
		path:  "/spec/template/spec/containers/0/securityContext",
		value: map[string]int{"runAsUser": 0},
	}, {
		kind: "Deployment",
		name: "otel-collector",
		op:   "add",
		path: "/spec/template/spec/containers/0/env",
		value: []map[string]any{{
			"name": "CLICKHOUSE_ADMIN_PASSWORD",
			"valueFrom": map[string]any{
				"secretKeyRef": map[string]any{
					"name":     "clickhouse-admin",
					"key":      "password",
					"optional": true,
				},
			},
		}},
	}, {
		kind: "Deployment",
		name: "otel-collector",
		path: "/spec/template/spec/containers/0/resources",
		value: map[string]any{
			"requests": map[string]string{
				"cpu":    "100m",
				"memory": "256Mi",
			},
			"limits": map[string]string{
				"memory": "512Mi",
			},
		},
	}, {
		kind: "Deployment",
		name: "otel-collector",
		op:   "add",
		path: "/spec/template/spec/containers/0/volumeMounts/-",
		value: map[string]any{
			"name":      "varlogpods",
			"mountPath": "/var/log/pods",
			"readOnly":  true,
		},
	}, {
		kind: "Deployment",
		name: "otel-collector",
		op:   "add",
		path: "/spec/template/spec/containers/0/volumeMounts/-",
		value: map[string]string{
			"name":      "checkpoints",
			"mountPath": "/var/lib/otel-collector",
		},
	}, {
		kind: "Deployment",
		name: "otel-collector",
		op:   "add",
		path: "/spec/template/spec/volumes/-",
		value: map[string]any{
			"name": "varlogpods",
			"hostPath": map[string]string{
				"path": "/var/log/pods",
			},
		},
	}, {
		kind: "Deployment",
		name: "otel-collector",
		op:   "add",
		path: "/spec/template/spec/volumes/-",
		value: map[string]any{
			"name": "checkpoints",
			"hostPath": map[string]string{
				"path": "/var/lib/otel-collector",
				"type": "DirectoryOrCreate",
			},
		},
	}}
}
