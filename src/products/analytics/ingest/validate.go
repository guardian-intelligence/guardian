package main

import (
	"encoding/json"
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
	maxPathLen     = 1024
	maxReferrerLen = 1024
	maxNameLen     = 64
	maxPropsLen    = 2048
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
	}
	knownVitals = map[string]struct{}{
		"LCP": {}, "CLS": {}, "INP": {}, "TTFB": {}, "FCP": {},
	}
)

type rejectReason string

const (
	rejectName     rejectReason = "name"
	rejectPath     rejectReason = "path"
	rejectReferrer rejectReason = "referrer"
	rejectTraceID  rejectReason = "trace_id"
	rejectVital    rejectReason = "vital"
	rejectProps    rejectReason = "props"
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

func validName(name string) bool {
	if len(name) == 0 || len(name) > maxNameLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' {
			continue
		}
		return false
	}
	return nameRegistered(name)
}

// validateEvent returns "" when the event is acceptable, else the reject
// reason. Validation never mutates the event.
func validateEvent(e *analyticsv1.Event) rejectReason {
	if !validName(e.GetName()) {
		return rejectName
	}
	if len(e.GetPath()) > maxPathLen || (e.GetPath() != "" && !strings.HasPrefix(e.GetPath(), "/")) {
		return rejectPath
	}
	if len(e.GetReferrer()) > maxReferrerLen {
		return rejectReferrer
	}
	// FixedString(16) zero-pads short values silently and aborts the insert
	// block on long ones — length must be exact or absent.
	if l := len(e.GetTraceId()); l != 0 && l != 16 {
		return rejectTraceID
	}
	if strings.HasPrefix(e.GetName(), "web_vital.") {
		if _, ok := knownVitals[e.GetVitalName()]; !ok {
			return rejectVital
		}
	} else if e.GetVitalName() != "" || e.GetVitalValue() != 0 {
		return rejectVital
	}
	if p := e.GetPropsJson(); p != "" {
		if len(p) > maxPropsLen || !json.Valid([]byte(p)) || !strings.HasPrefix(strings.TrimSpace(p), "{") {
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
