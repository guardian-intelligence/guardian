package main

import (
	"reflect"
	"testing"
	"time"
)

func TestParseNodes(t *testing.T) {
	got, err := parseNodes("ash-earth=10.8.0.11,ash-wind=10.8.0.12")
	if err != nil {
		t.Fatal(err)
	}
	want := []nodeTarget{
		{Name: "ash-earth", TalosNode: "10.8.0.11"},
		{Name: "ash-wind", TalosNode: "10.8.0.12"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseNodes() = %#v, want %#v", got, want)
	}
}

func TestKubeAPIServerForNodeUsesDifferentNode(t *testing.T) {
	got, err := kubeAPIServerForNode(
		"ash-earth=206.223.228.101,ash-wind=45.250.254.119,ash-water=206.223.228.87",
		"ash-earth",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://45.250.254.119:6443" {
		t.Fatalf("kubeAPIServerForNode() = %q, want alternate node API", got)
	}
}

func TestKubeAPIServerURLPreservesExplicitURL(t *testing.T) {
	got := kubeAPIServerURL("https://10.8.0.250:6443")
	if got != "https://10.8.0.250:6443" {
		t.Fatalf("kubeAPIServerURL() = %q", got)
	}
}

func TestSamplePayloadParsesRawJSON(t *testing.T) {
	got, ok := samplePayload(`{"event":"guardian_edge_failover_sample"}`)
	if !ok || got != `{"event":"guardian_edge_failover_sample"}` {
		t.Fatalf("samplePayload raw = %q %t", got, ok)
	}
}

func TestSamplePayloadParsesK6Logfmt(t *testing.T) {
	line := `time="2026-06-24T12:00:00Z" level=info msg="{\"event\":\"guardian_edge_failover_sample\"}" source=console`
	got, ok := samplePayload(line)
	if !ok || got != `{"event":"guardian_edge_failover_sample"}` {
		t.Fatalf("samplePayload logfmt = %q %t", got, ok)
	}
}

func TestOutageWindows(t *testing.T) {
	samples := []sample{
		{TimeUnixMS: 1000, OK: true},
		{TimeUnixMS: 1250, OK: false},
		{TimeUnixMS: 1500, OK: false},
		{TimeUnixMS: 1750, OK: true},
		{TimeUnixMS: 2000, OK: false},
		{TimeUnixMS: 2250, OK: true},
	}
	windows, failed := outageWindows(samples)
	if failed != 3 {
		t.Fatalf("failed = %d, want 3", failed)
	}
	want := []outageWindow{
		{StartUnixMS: 1250, EndUnixMS: 1750, DurationMS: 500, FailedSamples: 2},
		{StartUnixMS: 2000, EndUnixMS: 2250, DurationMS: 250, FailedSamples: 1},
	}
	if !reflect.DeepEqual(windows, want) {
		t.Fatalf("windows = %#v, want %#v", windows, want)
	}
}

func TestSummarizeNodeMaxOutage(t *testing.T) {
	started := time.UnixMilli(900)
	reboot := time.UnixMilli(1100)
	finished := time.UnixMilli(2400)
	report := summarizeNode(
		nodeTarget{Name: "ash-earth", TalosNode: "10.8.0.11"},
		[]sample{
			{TimeUnixMS: 1000, OK: true},
			{TimeUnixMS: 1250, OK: false},
			{TimeUnixMS: 1750, OK: true},
		},
		started,
		reboot,
		time.Time{},
		time.UnixMilli(2200),
		finished,
		true,
	)
	if report.MaxOutageMS != 500 {
		t.Fatalf("MaxOutageMS = %d, want 500", report.MaxOutageMS)
	}
	if report.NodeReadyUnixMS != 2200 {
		t.Fatalf("NodeReadyUnixMS = %d, want 2200", report.NodeReadyUnixMS)
	}
}
