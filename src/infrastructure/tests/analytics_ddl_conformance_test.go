package tests

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The deployed events DDL (deployments/analytics/system/ddl-configmap.yaml)
// is the Replicated form of the canonical single-node DDL
// (src/infrastructure/analytics/events-table.sql — the benched bake-off
// winner). Two copies invite silent drift: a column, codec, ORDER BY, or
// TTL edited in one place but not the other would quietly fork the schema
// the design doc's numbers were measured on. This test pins them together:
// after stripping comments and the replication-only differences (database
// prefix statement, ON CLUSTER, engine), the normalized bodies must match.
func TestAnalyticsEventsDDLConformance(t *testing.T) {
	canonical := readFileString(t, "src/infrastructure/analytics/events-table.sql")

	cmBytes, err := os.ReadFile(runfilePath("src/infrastructure/deployments/analytics/system/ddl-configmap.yaml"))
	if err != nil {
		t.Fatalf("read ddl-configmap.yaml: %v", err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(cmBytes, &cm); err != nil {
		t.Fatalf("parse ddl-configmap.yaml: %v", err)
	}
	deployed, ok := cm.Data["events.sql"]
	if !ok {
		t.Fatal("ddl-configmap.yaml has no events.sql key")
	}

	if !strings.Contains(deployed, "ReplicatedMergeTree('/clickhouse/tables/{shard}/guardian_analytics/events', '{replica}')") {
		t.Error("deployed DDL must use the explicit ReplicatedMergeTree path/replica args")
	}

	want := normalizeDDL(t, canonical)
	got := normalizeDDL(t, deployed)
	if want != got {
		t.Errorf("deployed events DDL drifted from canonical events-table.sql\ncanonical: %s\ndeployed:  %s", want, got)
	}
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(runfilePath(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

var (
	ddlComment    = regexp.MustCompile(`(?m)--[^\n]*$`)
	ddlCreateDB   = regexp.MustCompile(`(?is)CREATE DATABASE[^;]*;`)
	ddlOnCluster  = regexp.MustCompile(`(?i)ON CLUSTER '[^']*'`)
	ddlEngine     = regexp.MustCompile(`(?i)ENGINE\s*=\s*(ReplicatedMergeTree\([^)]*\)|MergeTree)`)
	ddlWhitespace = regexp.MustCompile(`\s+`)
)

// normalizeDDL reduces either DDL form to the shared schema substance:
// comments, the CREATE DATABASE statement, ON CLUSTER clauses, and the
// (deliberately different) engine spelling are removed; whitespace
// collapses.
func normalizeDDL(t *testing.T, sql string) string {
	t.Helper()
	s := ddlComment.ReplaceAllString(sql, "")
	s = ddlCreateDB.ReplaceAllString(s, "")
	s = ddlOnCluster.ReplaceAllString(s, "")
	if !ddlEngine.MatchString(s) {
		t.Fatal("DDL has no recognizable MergeTree engine clause")
	}
	s = ddlEngine.ReplaceAllString(s, "ENGINE = <mergetree>")
	s = ddlWhitespace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
