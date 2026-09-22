package tests

import (
	"strings"
	"testing"
)

// A slot invalidated at max_slot_wal_keep_size keeps a frozen restart_lsn, so
// its pg_wal_lsn_diff grows without bound while pinning nothing. Every slot
// rule reads the distance against the WAL actually on disk: retention and
// inactivity page only for slots whose WAL still exists, and a slot pointing
// past it is reported as lost.
func TestReplicationSlotRulesSeparateLostSlots(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/alerting/postgres-alerts.yaml")
	rules := map[string]map[string]interface{}{}
	for _, rawGroup := range sliceValue(nestedValue(t, singleYAMLDoc(t, path), "spec", "groups")) {
		for _, rawRule := range sliceValue(mapValue(rawGroup)["rules"]) {
			rule := mapValue(rawRule)
			rules[stringValue(rule["alert"])] = rule
		}
	}

	const walOnDisk = `(max by (namespace, pod) (cnpg_collector_pg_wal{value="size"}) + 67108864)`
	const lsnDiff = `(cnpg_pg_replication_slots_pg_wal_lsn_diff)`
	for alert, want := range map[string]struct {
		severity  string
		fragments []string
	}{
		"PGReplicationSlotWALRetained": {"critical", []string{
			lsnDiff + " > 2e9) <= on (namespace, pod) group_left () " + walOnDisk,
		}},
		"PGReplicationSlotInactive": {"critical", []string{
			"(cnpg_pg_replication_slots_active) == 0)",
			"unless on (namespace, pod, slot_name) (max by (namespace, pod, slot_name) " + lsnDiff + " > on (namespace, pod) group_left () " + walOnDisk + ")",
		}},
		"PGReplicationSlotLost": {"warning", []string{
			lsnDiff + " > on (namespace, pod) group_left () " + walOnDisk,
		}},
	} {
		rule, ok := rules[alert]
		if !ok {
			t.Fatalf("%s rule is missing", alert)
		}
		assertNestedString(t, rule, want.severity, "labels", "severity")
		// Folded scalars join lines with spaces; compare on single spaces.
		expr := strings.Join(strings.Fields(stringValue(rule["expr"])), " ")
		for _, fragment := range want.fragments {
			if !strings.Contains(expr, fragment) {
				t.Errorf("%s expression is missing %q: %s", alert, fragment, expr)
			}
		}
		if !strings.Contains(expr, "and on (namespace, pod) (cnpg_pg_replication_in_recovery == 0)") {
			t.Errorf("%s expression is not scoped to primaries: %s", alert, expr)
		}
	}
}
