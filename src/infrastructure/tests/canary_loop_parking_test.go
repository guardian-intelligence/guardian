package tests

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A suspended canary lane is parked with an expiry, and
// PostflightCanaryLoopSuspended stands down for exactly the parked lanes until
// that expiry. kube-state-metrics exports no CronJob annotations, so the rule
// carries its own copy of the ledger; this holds the copy to the source.
func TestCanaryLoopParkingMatchesSuspendedAlert(t *testing.T) {
	loopPath := runfilePath("src/infrastructure/deployments/postflight-runner/canary-loop.yaml")
	var parked []string
	parkedUntil := map[string]bool{}
	for _, doc := range yamlDocs(t, loopPath) {
		if stringValue(doc["kind"]) != "CronJob" {
			continue
		}
		metadata := mapValue(doc["metadata"])
		name := stringValue(metadata["name"])
		annotations := mapValue(metadata["annotations"])
		until := stringValue(annotations["guardian.dev/parked-until"])
		suspended, _ := mapValue(doc["spec"])["suspend"].(bool)

		if suspended && until == "" {
			t.Errorf("%s: CronJob %s is suspended without guardian.dev/parked-until", loopPath, name)
		}
		if until == "" {
			continue
		}
		if !suspended {
			t.Errorf("%s: CronJob %s is parked but not suspended; drop its parking annotations", loopPath, name)
		}
		if strings.TrimSpace(stringValue(annotations["guardian.dev/parked-reason"])) == "" {
			t.Errorf("%s: CronJob %s is parked without guardian.dev/parked-reason", loopPath, name)
		}
		parked = append(parked, name)
		parkedUntil[until] = true
	}
	if t.Failed() {
		return
	}
	if len(parked) == 0 {
		if strings.Contains(suspendedAlertExpr(t), "vector(time())") {
			t.Fatal("PostflightCanaryLoopSuspended still honours a parking ledger but no CronJob is parked")
		}
		return
	}
	if len(parkedUntil) != 1 {
		t.Fatalf("parked lanes carry %d distinct parked-until dates; the rule's ledger honours one", len(parkedUntil))
	}
	var until string
	for date := range parkedUntil {
		until = date
	}
	expiry, err := time.Parse("2006-01-02", until)
	if err != nil {
		t.Fatalf("guardian.dev/parked-until %q is not a YYYY-MM-DD date: %v", until, err)
	}

	expr := suspendedAlertExpr(t)
	ledger := regexp.MustCompile(`cronjob=~"([^"]+)"\}\)\s+and on \(\) \(vector\(time\(\)\) < (\d+)\)`).FindStringSubmatch(expr)
	if ledger == nil {
		t.Fatalf("PostflightCanaryLoopSuspended has no parking ledger for %v: %s", parked, expr)
	}
	honoured := strings.Split(ledger[1], "|")
	sort.Strings(honoured)
	sort.Strings(parked)
	if strings.Join(honoured, ",") != strings.Join(parked, ",") {
		t.Errorf("PostflightCanaryLoopSuspended honours parking for %v, want the parked CronJobs %v", honoured, parked)
	}
	epoch, err := strconv.ParseInt(ledger[2], 10, 64)
	if err != nil {
		t.Fatalf("parking expiry %q is not a Unix time: %v", ledger[2], err)
	}
	if got := time.Unix(epoch, 0).UTC(); !got.Equal(expiry) {
		t.Errorf("PostflightCanaryLoopSuspended parking expires at %s, want %s (guardian.dev/parked-until %s at UTC midnight = %d)",
			got.Format(time.RFC3339), expiry.Format(time.RFC3339), until, expiry.Unix())
	}
}

func suspendedAlertExpr(t *testing.T) string {
	t.Helper()

	path := runfilePath("src/infrastructure/deployments/postflight-runner/observability.yaml")
	for _, doc := range yamlDocs(t, path) {
		for _, group := range sliceValue(mapValue(doc["spec"])["groups"]) {
			for _, raw := range sliceValue(mapValue(group)["rules"]) {
				rule := mapValue(raw)
				if stringValue(rule["alert"]) == "PostflightCanaryLoopSuspended" {
					return stringValue(rule["expr"])
				}
			}
		}
	}
	t.Fatalf("%s: PostflightCanaryLoopSuspended rule is missing", path)
	return ""
}
