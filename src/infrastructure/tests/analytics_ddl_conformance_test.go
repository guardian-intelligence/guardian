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

// migrations.sql is the pending-ALTERs companion to events.sql: it restates
// column definitions the canonical file already declares, and a stale
// restatement would fork live tables from fresh bootstraps. Every
// ADD/MODIFY COLUMN definition (one action per line, AFTER clause and
// trailing punctuation stripped) must appear verbatim, whitespace-collapsed,
// in the canonical DDL.
func TestAnalyticsMigrationsMatchCanonical(t *testing.T) {
	canonical := ddlWhitespace.ReplaceAllString(
		ddlComment.ReplaceAllString(readFileString(t, "src/infrastructure/analytics/events-table.sql"), ""), " ")

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
	migrations, ok := cm.Data["migrations.sql"]
	if !ok {
		t.Skip("no migrations.sql key: nothing pending")
	}

	matched := 0
	for _, raw := range strings.Split(migrations, "\n") {
		line := strings.TrimSpace(raw)
		var def string
		switch {
		case strings.HasPrefix(line, "ADD COLUMN IF NOT EXISTS "):
			def = strings.TrimPrefix(line, "ADD COLUMN IF NOT EXISTS ")
		case strings.HasPrefix(line, "MODIFY COLUMN "):
			def = strings.TrimPrefix(line, "MODIFY COLUMN ")
		default:
			continue
		}
		matched++
		def = strings.TrimRight(def, ",;")
		def = ddlAfterClause.ReplaceAllString(def, "")
		def = strings.TrimSpace(ddlWhitespace.ReplaceAllString(def, " "))
		if !strings.Contains(canonical, def) {
			t.Errorf("migrations.sql definition %q not found in canonical events-table.sql", def)
		}
	}
	if matched == 0 {
		t.Error("migrations.sql present but no ADD/MODIFY COLUMN lines recognized; the drift guard is not seeing the statements")
	}
}

var ddlAfterClause = regexp.MustCompile(`(?i)\s+AFTER\s+\w+\s*$`)

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
