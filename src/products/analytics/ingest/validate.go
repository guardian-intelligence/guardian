package main

import (
	"encoding/json"
	"math"
	"strings"

	analyticsv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/analytics/v1"
)

// Value-level enforcement. connect-go's JSON codec hardcodes
// DiscardUnknown, so unknown wire fields cannot be rejected at the codec —
// everything the schema means is enforced here (recorded in
// docs/analytics-storage-design.md). Rejects are counted per reason and
// surface in PublishResponse; schema fuzzing is a fraud signal.

const (
	maxBatchEvents = 500
	// A batch's sent_at more than this far from server receipt marks the
	// whole batch's skew as untrustworthy; skew is clamped, never rejected
	// (broken client clocks are a signal worth storing, not dropping).
	maxAbsSkewMs = 7 * 24 * 3600 * 1000
)

// Registered event vocabulary: exact names and owned prefixes. Everything
// the site emits today (lib/telemetry call sites) plus the beacon's own
// lifecycle events. Unknown names reject the event.
var (
	registeredNames = map[string]struct{}{
		"page_view":     {},
		"click":         {},
		"error":         {},
		"outbound_link": {},
		"scroll_depth":  {},
	}
	registeredPrefixes = []string{
		"company.",
		"newsroom.",
		"design.",
		"web_vital.",
		"app_chrome.",
		"page_shell.",
		"beacon.",
		"shortty.",
	}
	knownVitals = map[string]struct{}{
		"LCP": {}, "CLS": {}, "INP": {}, "TTFB": {}, "FCP": {},
	}
)

type rejectReason string

const (
	// Structural violations (length, pattern, trace_id size) are reported by
	// protovalidate under this reason; the field is in the log detail.
	rejectSchema rejectReason = "schema"
	rejectName   rejectReason = "name"
	rejectVital  rejectReason = "vital"
	rejectProps  rejectReason = "props"
)

func nameRegistered(name string) bool {
	if _, ok := registeredNames[name]; ok {
		return true
	}
	for _, p := range registeredPrefixes {
		if strings.HasPrefix(name, p) && len(name) > len(p) {
			return true
		}
	}
	return false
}

// validateEvent covers the checks that are not expressible as protovalidate
// field constraints: the event-name registry, the web-vital cross-field
// rules, and props JSON-object shape. It runs after protovalidate has
// enforced the structural constraints, and never mutates the event.
func validateEvent(e *analyticsv1.Event) rejectReason {
	if !nameRegistered(e.GetName()) {
		return rejectName
	}
	if strings.HasPrefix(e.GetName(), "web_vital.") {
		if _, ok := knownVitals[e.GetVitalName()]; !ok {
			return rejectVital
		}
		// protojson accepts "NaN"/"Infinity" strings for doubles; one NaN
		// row poisons every avg/quantile over the vital. Bound to plausible
		// physics: CLS is unitless [0,10], the rest are ms under ~10min.
		v := e.GetVitalValue()
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return rejectVital
		}
		if e.GetVitalName() == "CLS" {
			if v > 10 {
				return rejectVital
			}
		} else if v > 600_000 {
			return rejectVital
		}
	} else if e.GetVitalName() != "" || e.GetVitalValue() != 0 {
		return rejectVital
	}
	if p := e.GetPropsJson(); p != "" {
		if !json.Valid([]byte(p)) || !strings.HasPrefix(strings.TrimSpace(p), "{") {
			return rejectProps
		}
	}
	return ""
}

// clampSkewMs derives client_skew_ms = received_at - sent_at, clamped to
// int32 and to the plausible-window bound.
func clampSkewMs(receivedUnixMs int64, sentAtUnixMs uint64) int32 {
	if sentAtUnixMs == 0 || sentAtUnixMs > 1<<62 {
		return 0
	}
	skew := receivedUnixMs - int64(sentAtUnixMs)
	if skew > maxAbsSkewMs {
		skew = maxAbsSkewMs
	}
	if skew < -maxAbsSkewMs {
		skew = -maxAbsSkewMs
	}
	return int32(skew)
}
