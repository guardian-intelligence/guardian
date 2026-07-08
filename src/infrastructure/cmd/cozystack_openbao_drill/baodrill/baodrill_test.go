package baodrill

import (
	"strings"
	"testing"
)

func status(initialized, sealed, haEnabled bool, clusterID, version string) baoStatus {
	return baoStatus{
		Initialized: initialized,
		Sealed:      sealed,
		HAEnabled:   haEnabled,
		ClusterID:   clusterID,
		Version:     version,
	}
}

func TestValidateStatusSet(t *testing.T) {
	statuses := []podBaoStatus{
		{Pod: "guardian-openbao-0", Status: status(true, false, true, "cluster-a", "2.5.4")},
		{Pod: "guardian-openbao-1", Status: status(true, false, true, "cluster-a", "2.5.4")},
		{Pod: "guardian-openbao-2", Status: status(true, false, true, "cluster-a", "2.5.4")},
	}
	if err := validateStatusSet(statuses, "2.5.4"); err != nil {
		t.Fatalf("validateStatusSet() error = %v", err)
	}
}

func TestValidateStatusSetRejectsMixedClusterIDs(t *testing.T) {
	statuses := []podBaoStatus{
		{Pod: "guardian-openbao-0", Status: status(true, false, true, "cluster-a", "2.5.4")},
		{Pod: "guardian-openbao-1", Status: status(true, false, true, "cluster-b", "2.5.4")},
	}
	err := validateStatusSet(statuses, "2.5.4")
	if err == nil {
		t.Fatalf("validateStatusSet() accepted mixed cluster IDs")
	}
	if !strings.Contains(err.Error(), "cluster_id=") {
		t.Fatalf("validateStatusSet() error = %v, want cluster ID detail", err)
	}
}

func TestValidateStatusSetRejectsSealedPod(t *testing.T) {
	statuses := []podBaoStatus{
		{Pod: "guardian-openbao-0", Status: status(true, true, true, "cluster-a", "2.5.4")},
	}
	err := validateStatusSet(statuses, "2.5.4")
	if err == nil {
		t.Fatalf("validateStatusSet() accepted a sealed pod")
	}
	if !strings.Contains(err.Error(), "is sealed") {
		t.Fatalf("validateStatusSet() error = %v, want sealed detail", err)
	}
}

func TestValidateStatusSetRejectsHADisabled(t *testing.T) {
	statuses := []podBaoStatus{
		{Pod: "guardian-openbao-0", Status: status(true, false, false, "cluster-a", "2.5.4")},
	}
	err := validateStatusSet(statuses, "2.5.4")
	if err == nil {
		t.Fatalf("validateStatusSet() accepted ha_enabled=false")
	}
	if !strings.Contains(err.Error(), "ha_enabled=false") {
		t.Fatalf("validateStatusSet() error = %v, want HA detail", err)
	}
}

func TestValidateStatusSetRejectsEmptyClusterID(t *testing.T) {
	statuses := []podBaoStatus{
		{Pod: "guardian-openbao-0", Status: status(true, false, true, "", "2.5.4")},
	}
	err := validateStatusSet(statuses, "2.5.4")
	if err == nil {
		t.Fatalf("validateStatusSet() accepted empty cluster_id")
	}
	if !strings.Contains(err.Error(), "empty cluster_id") {
		t.Fatalf("validateStatusSet() error = %v, want cluster ID detail", err)
	}
}

func TestValidateStatusSetRejectsVersionMismatch(t *testing.T) {
	statuses := []podBaoStatus{
		{Pod: "guardian-openbao-0", Status: status(true, false, true, "cluster-a", "2.5.3")},
	}
	err := validateStatusSet(statuses, "2.5.4")
	if err == nil {
		t.Fatalf("validateStatusSet() accepted version mismatch")
	}
	if !strings.Contains(err.Error(), "expected 2.5.4") {
		t.Fatalf("validateStatusSet() error = %v, want version detail", err)
	}
}

func TestOpenBaoVersionFromImage(t *testing.T) {
	version, err := openBaoVersionFromImage("ghcr.io/openbao/openbao:2.5.4@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("openBaoVersionFromImage() error = %v", err)
	}
	if version != "2.5.4" {
		t.Fatalf("openBaoVersionFromImage() = %q, want 2.5.4", version)
	}
}

func TestParseBaoStatusFromNoisyOutput(t *testing.T) {
	raw := "some kubectl noise\n{\"initialized\":true,\"sealed\":false,\"ha_enabled\":true,\"cluster_id\":\"cluster-a\",\"version\":\"2.5.4\"}\n"
	parsed, err := parseBaoStatus(raw)
	if err != nil {
		t.Fatalf("parseBaoStatus() error = %v", err)
	}
	if !parsed.Initialized || parsed.Sealed || !parsed.HAEnabled || parsed.ClusterID != "cluster-a" || parsed.Version != "2.5.4" {
		t.Fatalf("parseBaoStatus() = %+v, want initialized unsealed HA cluster-a 2.5.4", parsed)
	}
}
