package main

import (
	"math"
	"strings"
	"testing"

	"buf.build/go/protovalidate"

	analyticsv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/analytics/v1"
)

// validateEvent covers the semantic checks that are not protovalidate field
// constraints: the name registry, web-vital cross-field rules, props JSON shape.
func TestValidateEvent(t *testing.T) {
	ok := func(e *analyticsv1.Event) *analyticsv1.Event { return e }
	cases := []struct {
		name   string
		event  *analyticsv1.Event
		reject rejectReason
	}{
		{"registered exact name", ok(&analyticsv1.Event{Name: "page_view", Path: "/"}), ""},
		{"registered prefix name", ok(&analyticsv1.Event{Name: "company.route_view", Path: "/letters"}), ""},
		{"unregistered name", &analyticsv1.Event{Name: "cryptominer.ping"}, rejectName},
		{"prefix alone is not a name", &analyticsv1.Event{Name: "company."}, rejectName},
		{"vital without web_vital name", &analyticsv1.Event{Name: "page_view", VitalName: "LCP"}, rejectVital},
		{"web_vital with unknown vital", &analyticsv1.Event{Name: "web_vital.lcp", VitalName: "BOGUS"}, rejectVital},
		{"web_vital ok", ok(&analyticsv1.Event{Name: "web_vital.lcp", VitalName: "LCP", VitalValue: 1234.5}), ""},
		{"vital NaN rejected", &analyticsv1.Event{Name: "web_vital.lcp", VitalName: "LCP", VitalValue: math.NaN()}, rejectVital},
		{"vital +Inf rejected", &analyticsv1.Event{Name: "web_vital.lcp", VitalName: "LCP", VitalValue: math.Inf(1)}, rejectVital},
		{"vital negative rejected", &analyticsv1.Event{Name: "web_vital.inp", VitalName: "INP", VitalValue: -5}, rejectVital},
		{"vital ms over bound", &analyticsv1.Event{Name: "web_vital.lcp", VitalName: "LCP", VitalValue: 1e9}, rejectVital},
		{"CLS over bound", &analyticsv1.Event{Name: "web_vital.cls", VitalName: "CLS", VitalValue: 42}, rejectVital},
		{"CLS in range ok", ok(&analyticsv1.Event{Name: "web_vital.cls", VitalName: "CLS", VitalValue: 0.17}), ""},
		{"props invalid json", &analyticsv1.Event{Name: "click", PropsJson: "{oops"}, rejectProps},
		{"props not an object", &analyticsv1.Event{Name: "click", PropsJson: `[1,2]`}, rejectProps},
		{"props ok", ok(&analyticsv1.Event{Name: "click", PropsJson: `{"target":"cta"}`}), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateEvent(tc.event); got != tc.reject {
				t.Fatalf("validateEvent() = %q, want %q", got, tc.reject)
			}
		})
	}
}

// The structural constraints declared on events.proto are enforced by
// protovalidate — this checks they are wired and behave.
func TestSchemaConstraints(t *testing.T) {
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		event   *analyticsv1.Event
		wantErr bool
	}{
		{"valid", &analyticsv1.Event{Name: "page_view", Path: "/", TraceId: make([]byte, 16)}, false},
		{"empty trace ok", &analyticsv1.Event{Name: "page_view", Path: "/"}, false},
		{"empty name", &analyticsv1.Event{Name: ""}, true},
		{"uppercase name", &analyticsv1.Event{Name: "PAGE_VIEW"}, true},
		{"name over 64", &analyticsv1.Event{Name: "company." + strings.Repeat("a", 64)}, true},
		{"path not rooted", &analyticsv1.Event{Name: "page_view", Path: "javascript:alert(1)"}, true},
		{"path over 1024", &analyticsv1.Event{Name: "page_view", Path: "/" + strings.Repeat("a", 1024)}, true},
		{"referrer over 1024", &analyticsv1.Event{Name: "page_view", Referrer: strings.Repeat("r", 1025)}, true},
		{"trace id wrong length", &analyticsv1.Event{Name: "page_view", TraceId: []byte("short")}, true},
		{"props over 2048", &analyticsv1.Event{Name: "click", PropsJson: strings.Repeat("x", 2049)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := v.Validate(tc.event)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
func TestClampSkew(t *testing.T) {
	now := int64(1_800_000_000_000)
	cases := []struct {
		name string
		sent uint64
		want int32
	}{
		{"zero sent_at", 0, 0},
		{"honest small skew", uint64(now - 1500), 1500},
		{"future clock", uint64(now + 4000), -4000},
		{"absurd past clamps", uint64(now) - 1<<40, maxAbsSkewMs},
		{"absurd future clamps", uint64(now) + 1<<40, -maxAbsSkewMs},
		{"garbage huge value", 1 << 63, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampSkewMs(now, tc.sent); got != tc.want {
				t.Fatalf("clampSkewMs() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSiteFromHost(t *testing.T) {
	cases := map[string]string{
		"guardianintelligence.org":         "prod",
		"www.guardianintelligence.org":     "prod",
		"GUARDIANINTELLIGENCE.ORG":         "prod",
		"guardianintelligence.org:443":     "prod",
		"pr-42.guardianintelligence.org":   "pr-42",
		"beta.guardianintelligence.org":    "beta",
		"evil.example.com":                 "local",
		"guardianintelligence.org.evil.io": "local",
		"10.0.0.5:8080":                    "local",
		"":                                 "local",
	}
	for host, want := range cases {
		if got := siteFromHost(host); got != want {
			t.Errorf("siteFromHost(%q) = %q, want %q", host, got, want)
		}
	}
}
