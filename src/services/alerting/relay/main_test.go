package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// alertaPayload mirrors what Alerta's slack plugin (alerta-contrib
// alerta_slack.py) actually posts under the Cozystack v1.5.0 chart, where
// only SLACK_WEBHOOK_URL is set and SLACK_ATTACHMENTS defaults to False: a
// TEXT-ONLY message rendered from SLACK_DEFAULT_SUMMARY_FMT — no
// attachments, no fields, no color.
const alertaPayload = `{
	"username": "alerta",
	"channel": "",
	"text": "*[Open] PRODUCTION Guardian Critical* - _KubePodCrashLooping on tenant-root/openbao-0_ <http://alerta.tenant-root.svc/#/alert/9d4c8a1e-5b2f-4f4e-9c9f-1c2c3d4e5f6a|9d4c8a1e>"
}`

// alertaText renders the plugin's default summary line the way
// alerta_slack.py does: status/severity capitalized upstream, environment
// uppercased, service comma-joined, event/resource raw, trailing dashboard
// link.
func alertaText(status, env, service, severity, event, resource string) string {
	return "*[" + status + "] " + env + " " + service + " " + severity + "* - _" +
		event + " on " + resource + "_ <http://alerta.tenant-root.svc/#/alert/9d4c8a1e-5b2f-4f4e-9c9f-1c2c3d4e5f6a|9d4c8a1e>"
}

func alertaJSON(text string) string {
	b, _ := json.Marshal(map[string]string{"username": "alerta", "channel": "", "text": text})
	return string(b)
}

// flaggerPayload mirrors a Flagger AlertProvider slack message, verified
// against flagger v1.43.0 pkg/notifier/slack.go Post(): Flagger genuinely
// sends attachments — no top-level text (omitempty, never set), one
// attachment with color good|danger (danger when severity=="error"),
// author_name "{workload}.{namespace}", the message in attachment text, and
// fields with short=false.
const flaggerPayload = `{
	"channel": "flagger",
	"username": "flagger",
	"icon_url": "",
	"icon_emoji": ":rocket:",
	"attachments": [{
		"color": "danger",
		"author_name": "keycloak.guardian-iam-beta",
		"text": "Canary analysis failed, rolling back.",
		"mrkdwn_in": ["text"],
		"fields": [
			{"title": "Target", "value": "Deployment/keycloak", "short": false},
			{"title": "Failed checks threshold", "value": "2", "short": false}
		]
	}]
}`

func TestCompose(t *testing.T) {
	cases := []struct {
		name         string
		payload      string
		wantTitle    string
		wantPriority int
		wantTags     []string
		wantInBody   []string
	}{
		{
			name:         "alerta text-only open critical",
			payload:      alertaPayload,
			wantTitle:    "KubePodCrashLooping on tenant-root/openbao-0",
			wantPriority: 5,
			wantTags:     []string{"critical", "production"},
			wantInBody:   []string{"*[Open] PRODUCTION Guardian Critical*", "KubePodCrashLooping on tenant-root/openbao-0"},
		},
		{
			name:         "alerta text-only closed ok pages low with status prefix",
			payload:      alertaJSON(alertaText("Closed", "PRODUCTION", "Guardian", "Ok", "KubePodCrashLooping", "tenant-root/openbao-0")),
			wantTitle:    "[Closed] KubePodCrashLooping on tenant-root/openbao-0",
			wantPriority: 2,
			wantTags:     []string{"ok", "production"},
			wantInBody:   []string{"*[Closed] PRODUCTION Guardian Ok*"},
		},
		{
			name:         "alerta text-only comma-joined multi-word service list",
			payload:      alertaJSON(alertaText("Open", "BETA", "Keycloak,Guardian Web", "Major", "HighErrorRate", "guardian-iam-beta/keycloak-0")),
			wantTitle:    "HighErrorRate on guardian-iam-beta/keycloak-0",
			wantPriority: 4,
			wantTags:     []string{"major", "beta"},
			wantInBody:   []string{"Keycloak,Guardian Web"},
		},
		{
			// The event itself contains " on ": the parser must split the
			// italic segment at the LAST " on " so the resource stays a
			// namespace/pod path.
			name:         "alerta text-only event containing ' on ' splits at the last one",
			payload:      alertaJSON(alertaText("Open", "PRODUCTION", "Guardian", "Warning", "FailedMount on startup", "tenant-root/openbao-0")),
			wantTitle:    "FailedMount on startup on tenant-root/openbao-0",
			wantPriority: 3,
			wantTags:     []string{"warning", "production"},
			wantInBody:   []string{"FailedMount on startup"},
		},
		{
			name:         "text-only payload that is not an alerta summary keeps the fallback",
			payload:      `{"username":"someone","text":"free-form message\nwith a second line"}`,
			wantTitle:    "free-form message",
			wantPriority: 3,
			wantTags:     nil,
			wantInBody:   []string{"free-form message", "with a second line"},
		},
		{
			name:         "flagger style titles from attachment author_name",
			payload:      flaggerPayload,
			wantTitle:    "keycloak.guardian-iam-beta",
			wantPriority: 5,
			wantTags:     nil,
			wantInBody:   []string{"Canary analysis failed, rolling back.", "Target: Deployment/keycloak", "Failed checks threshold: 2"},
		},
		{
			name:         "event and resource fields form the title when attachment title absent",
			payload:      `{"attachments":[{"fields":[{"title":"event","value":"DiskFull"},{"title":"resource","value":"node-3"},{"title":"severity","value":"major"}]}]}`,
			wantTitle:    "DiskFull node-3",
			wantPriority: 4,
			wantTags:     []string{"major"},
			wantInBody:   []string{"event: DiskFull"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParse(t, tc.payload)
			n := compose(p)
			if n.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", n.Title, tc.wantTitle)
			}
			if n.Priority != tc.wantPriority {
				t.Errorf("priority = %d, want %d", n.Priority, tc.wantPriority)
			}
			if len(n.Tags) != len(tc.wantTags) {
				t.Errorf("tags = %v, want %v", n.Tags, tc.wantTags)
			} else {
				for i := range tc.wantTags {
					if n.Tags[i] != tc.wantTags[i] {
						t.Errorf("tags = %v, want %v", n.Tags, tc.wantTags)
						break
					}
				}
			}
			for _, want := range tc.wantInBody {
				if !strings.Contains(n.Body, want) {
					t.Errorf("body missing %q:\n%s", want, n.Body)
				}
			}
		})
	}
}

// TestParseAlertaSummary pins the parsing contract against
// SLACK_DEFAULT_SUMMARY_FMT as alerta_slack.py renders it.
func TestParseAlertaSummary(t *testing.T) {
	cases := []struct {
		name string
		text string
		want alertaSummary
		ok   bool
	}{
		{
			name: "full summary with dashboard link",
			text: "*[Open] PRODUCTION Guardian Critical* - _KubePodCrashLooping on tenant-root/openbao-0_ <http://alerta.tenant-root.svc/#/alert/9d4c8a1e-5b2f-4f4e-9c9f-1c2c3d4e5f6a|9d4c8a1e>",
			want: alertaSummary{status: "Open", environment: "PRODUCTION", service: "Guardian", severity: "Critical", event: "KubePodCrashLooping", resource: "tenant-root/openbao-0"},
			ok:   true,
		},
		{
			name: "empty DASHBOARD_URL still emits the link wrapper",
			text: "*[Ack] BETA Keycloak Major* - _HighErrorRate on guardian-iam-beta/keycloak-0_ </#/alert/9d4c8a1e-5b2f-4f4e-9c9f-1c2c3d4e5f6a|9d4c8a1e>",
			want: alertaSummary{status: "Ack", environment: "BETA", service: "Keycloak", severity: "Major", event: "HighErrorRate", resource: "guardian-iam-beta/keycloak-0"},
			ok:   true,
		},
		{
			name: "comma-joined service list with a space in a service name",
			text: "*[Open] BETA Keycloak,Guardian Web Major* - _HighErrorRate on guardian-iam-beta/keycloak-0_ <http://a/#/alert/x|x>",
			want: alertaSummary{status: "Open", environment: "BETA", service: "Keycloak,Guardian Web", severity: "Major", event: "HighErrorRate", resource: "guardian-iam-beta/keycloak-0"},
			ok:   true,
		},
		{
			name: "empty service list renders adjacent spaces",
			text: "*[Open] PRODUCTION  Critical* - _KubePodCrashLooping on tenant-root/openbao-0_ <http://a/#/alert/x|x>",
			want: alertaSummary{status: "Open", environment: "PRODUCTION", service: "", severity: "Critical", event: "KubePodCrashLooping", resource: "tenant-root/openbao-0"},
			ok:   true,
		},
		{
			name: "event containing ' on ' splits at the last occurrence",
			text: "*[Open] PRODUCTION Guardian Warning* - _FailedMount on startup on tenant-root/openbao-0_ <http://a/#/alert/x|x>",
			want: alertaSummary{status: "Open", environment: "PRODUCTION", service: "Guardian", severity: "Warning", event: "FailedMount on startup", resource: "tenant-root/openbao-0"},
			ok:   true,
		},
		{name: "free-form text does not match", text: "Canary analysis failed, rolling back."},
		{name: "empty text does not match", text: ""},
		{name: "bold header without italic segment does not match", text: "*[Open] PRODUCTION Guardian Critical* - something else"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseAlertaSummary(tc.text)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Errorf("parsed = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPriorityFor(t *testing.T) {
	cases := []struct {
		severity, color string
		want            int
	}{
		{"critical", "", 5},
		{"", "danger", 5},
		{"major", "", 4},
		{"warning", "", 3},
		{"minor", "", 3},
		{"informational", "", 2},
		{"indeterminate", "", 2},
		{"cleared", "", 2},
		{"normal", "", 2},
		{"", "ok", 2},
		{"", "good", 2},
		{"WARNING", "", 3},
		{"", "", 3},
		{"weird", "#36a64f", 3},
		{"critical", "good", 5}, // severity wins over color
	}
	for _, tc := range cases {
		if got := priorityFor(tc.severity, tc.color); got != tc.want {
			t.Errorf("priorityFor(%q, %q) = %d, want %d", tc.severity, tc.color, got, tc.want)
		}
	}
}

type fakeSink struct {
	sent []notification
	err  error
}

func (f *fakeSink) deliver(_ context.Context, n notification) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, n)
	return nil
}

type funcSink struct {
	fn func(context.Context, notification) error
}

func (f *funcSink) deliver(ctx context.Context, n notification) error { return f.fn(ctx, n) }

func newTestServer(out sink) *server {
	return &server{cfg: config{}, m: &metrics{}, out: out}
}

func TestHandleSlackSuppressesHeartbeats(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		suppress bool
	}{
		{"watchdog event field", `{"attachments":[{"fields":[{"title":"event","value":"Watchdog"}]}]}`, true},
		{"heartbeat alertname field", `{"attachments":[{"fields":[{"title":"alertname","value":"Heartbeat"}]}]}`, true},
		{"case insensitive", `{"attachments":[{"fields":[{"title":"event","value":"WATCHDOG"}]}]}`, true},
		{"real alert forwards", `{"attachments":[{"fields":[{"title":"event","value":"DiskFull"}]}]}`, false},
		// Text-only Alerta summaries: a repeat=False receive of the
		// always-firing Watchdog (fresh DB post-DR, re-created alert) must be
		// suppressed on the parsed event, not forwarded as a stray page.
		{"alerta text-only watchdog", alertaJSON(alertaText("Open", "PRODUCTION", "Guardian", "Informational", "Watchdog", "vmalert")), true},
		{"alerta text-only heartbeat case insensitive", alertaJSON(alertaText("Open", "PRODUCTION", "Guardian", "Informational", "HEARTBEAT", "alerta")), true},
		{"alerta text-only real alert forwards", alertaPayload, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := &fakeSink{}
			s := newTestServer(out)
			w := httptest.NewRecorder()
			s.handleSlack(w, httptest.NewRequest("POST", "/slack", strings.NewReader(tc.payload)))
			s.inflight.Wait()
			if tc.suppress {
				if w.Code != 200 {
					t.Fatalf("status = %d, want 200", w.Code)
				}
				if len(out.sent) != 0 {
					t.Errorf("suppressed payload was forwarded: %+v", out.sent)
				}
				if got := s.m.suppressed.Load(); got != 1 {
					t.Errorf("suppressed counter = %d, want 1", got)
				}
			} else {
				if w.Code != 202 {
					t.Fatalf("status = %d, want 202", w.Code)
				}
				if len(out.sent) != 1 {
					t.Fatalf("forwarded %d notifications, want 1", len(out.sent))
				}
				if got := s.m.forwarded.Load(); got != 1 {
					t.Errorf("forwarded counter = %d, want 1", got)
				}
			}
		})
	}
}

func TestHandleSlackParseFallback(t *testing.T) {
	out := &fakeSink{}
	s := newTestServer(out)
	long := "this is not json " + strings.Repeat("x", 8192)
	w := httptest.NewRecorder()
	s.handleSlack(w, httptest.NewRequest("POST", "/slack", strings.NewReader(long)))
	s.inflight.Wait()
	if w.Code != 202 {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	if len(out.sent) != 1 {
		t.Fatalf("forwarded %d notifications, want 1", len(out.sent))
	}
	n := out.sent[0]
	if n.Title != "unparsed alert" {
		t.Errorf("title = %q, want \"unparsed alert\"", n.Title)
	}
	if n.Priority != 3 {
		t.Errorf("priority = %d, want 3", n.Priority)
	}
	if len(n.Body) != 4096 {
		t.Errorf("body length = %d, want 4096 (truncated)", len(n.Body))
	}
	if !strings.HasPrefix(n.Body, "this is not json ") {
		t.Errorf("body does not carry the raw payload: %q", n.Body[:32])
	}
}

func TestHandleSlackDeliveryFailure(t *testing.T) {
	out := &fakeSink{err: errors.New("ntfy down")}
	s := newTestServer(out)
	w := httptest.NewRecorder()
	s.handleSlack(w, httptest.NewRequest("POST", "/slack", strings.NewReader(alertaPayload)))
	// The payload is accepted before delivery; failure surfaces via metrics.
	if w.Code != 202 {
		t.Errorf("status = %d, want 202", w.Code)
	}
	s.inflight.Wait()
	if got := s.m.forwardFailures.Load(); got != 1 {
		t.Errorf("failure counter = %d, want 1", got)
	}
	if got := s.m.forwarded.Load(); got != 0 {
		t.Errorf("forwarded counter = %d, want 0", got)
	}
}

func TestHandleSlackSelfAlertDeliveryFailureDoesNotFeedFailureAlert(t *testing.T) {
	out := &fakeSink{err: errors.New("ntfy down")}
	s := newTestServer(out)
	payload := alertaJSON(alertaText(
		"Open",
		"PRODUCTION",
		"Guardian",
		"Warning",
		"AlertRelayForwardFailures",
		"10.244.0.230:8080",
	))
	w := httptest.NewRecorder()
	s.handleSlack(w, httptest.NewRequest("POST", "/slack", strings.NewReader(payload)))
	if w.Code != 202 {
		t.Errorf("status = %d, want 202", w.Code)
	}
	s.inflight.Wait()
	if got := s.m.forwardFailures.Load(); got != 0 {
		t.Errorf("failure counter = %d, want 0", got)
	}
	if got := s.m.selfAlertFailures.Load(); got != 1 {
		t.Errorf("self-alert failure counter = %d, want 1", got)
	}
}

// TestHandleSlackSurvivesProducerDisconnect drives the real ntfySink through
// the handler against a sink that fails twice, and cancels the producer's
// request context during the first attempt — as Flagger's 5s-timeout,
// no-retry alert client does. All three attempts must still run.
func TestHandleSlackSurvivesProducerDisconnect(t *testing.T) {
	var attempts atomic.Int32
	rctx, rcancel := context.WithCancel(context.Background())
	defer rcancel()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch attempts.Add(1) {
		case 1:
			rcancel() // producer hangs up mid-delivery
			w.WriteHeader(http.StatusInternalServerError)
		case 2:
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer up.Close()

	out := &ntfySink{url: up.URL, client: up.Client(), sleep: func(context.Context, time.Duration) {}}
	s := newTestServer(out)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/slack", strings.NewReader(alertaPayload)).WithContext(rctx)
	s.handleSlack(w, req)
	if w.Code != 202 {
		t.Fatalf("status = %d, want 202", w.Code)
	}
	s.inflight.Wait()
	if got := attempts.Load(); got != 3 {
		t.Errorf("sink attempts = %d, want 3 (producer disconnect must not cancel retries)", got)
	}
	if got := s.m.forwarded.Load(); got != 1 {
		t.Errorf("forwarded counter = %d, want 1", got)
	}
	if got := s.m.forwardFailures.Load(); got != 0 {
		t.Errorf("failure counter = %d, want 0", got)
	}
}

// TestShutdownWaitsForInflightDelivery pins the shutdown contract: the
// inflight waitgroup main blocks on at exit must not release while an
// accepted alert is still delivering.
func TestShutdownWaitsForInflightDelivery(t *testing.T) {
	release := make(chan struct{})
	out := &funcSink{fn: func(_ context.Context, _ notification) error {
		<-release
		return nil
	}}
	s := newTestServer(out)
	w := httptest.NewRecorder()
	s.handleSlack(w, httptest.NewRequest("POST", "/slack", strings.NewReader(alertaPayload)))
	if w.Code != 202 {
		t.Fatalf("status = %d, want 202", w.Code)
	}

	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("inflight.Wait returned while delivery was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("inflight.Wait did not return after delivery finished")
	}
	if got := s.m.forwarded.Load(); got != 1 {
		t.Errorf("forwarded counter = %d, want 1", got)
	}
}

// TestNtfySinkSanitizesHeaders asserts the exact outbound header values: any
// control byte (<0x20 or 0x7F) in alert-derived content becomes a space, so
// the transport never rejects the request.
func TestNtfySinkSanitizesHeaders(t *testing.T) {
	cases := []struct {
		name      string
		n         notification
		wantTitle string
		wantTags  string
	}{
		{
			name:      "newline and carriage return",
			n:         notification{Title: "line1\r\nline2", Priority: 5, Tags: []string{"crit\nical", "prod\ruction"}, Body: "b"},
			wantTitle: "line1  line2",
			wantTags:  "crit ical,prod uction",
		},
		{
			name:      "low control bytes and DEL",
			n:         notification{Title: "a\x00b\x1fc\x7fd", Priority: 3, Tags: []string{"t\x01a\x08g"}, Body: "b"},
			wantTitle: "a b c d",
			wantTags:  "t a g",
		},
		{
			name:      "clean values pass through unchanged",
			n:         notification{Title: "PodCrashLooping on tenant-root/openbao-0", Priority: 5, Tags: []string{"critical", "production"}, Body: "b"},
			wantTitle: "PodCrashLooping on tenant-root/openbao-0",
			wantTags:  "critical,production",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotTitle, gotTags string
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotTitle = r.Header.Get("X-Title")
				gotTags = r.Header.Get("X-Tags")
				w.WriteHeader(http.StatusOK)
			}))
			defer up.Close()
			s := &ntfySink{url: up.URL, client: up.Client(), sleep: func(context.Context, time.Duration) {}}
			if err := s.deliver(context.Background(), tc.n); err != nil {
				t.Fatalf("deliver: %v", err)
			}
			if gotTitle != tc.wantTitle {
				t.Errorf("X-Title = %q, want %q", gotTitle, tc.wantTitle)
			}
			if gotTags != tc.wantTags {
				t.Errorf("X-Tags = %q, want %q", gotTags, tc.wantTags)
			}
		})
	}
}

func TestDeadmanStateMachine(t *testing.T) {
	start := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	d := newDeadman(start, 10*time.Minute, time.Hour)
	at := func(offset time.Duration) time.Time { return start.Add(offset) }

	steps := []struct {
		name   string
		offset time.Duration
		active bool
		want   deadmanAction
	}{
		{"active watchdog is quiet", 1 * time.Minute, true, dmNone},
		{"brief silence under timeout", 2 * time.Minute, false, dmNone},
		{"silence at 9m under timeout", 10 * time.Minute, false, dmNone},
		{"silence crosses timeout pages", 12 * time.Minute, false, dmPage},
		{"still silent, no repage yet", 30 * time.Minute, false, dmNone},
		{"hour after page repages", 73 * time.Minute, false, dmRepage},
		{"still silent after repage", 90 * time.Minute, false, dmNone},
		{"second hourly repage", 134 * time.Minute, false, dmRepage},
		{"watchdog returns recovers", 140 * time.Minute, true, dmRecover},
		{"healthy again is quiet", 141 * time.Minute, true, dmNone},
		{"new silence restarts the clock", 150 * time.Minute, false, dmNone},
		{"new timeout pages again", 151 * time.Minute, false, dmPage},
	}
	for _, s := range steps {
		got := d.observe(at(s.offset), s.active)
		if got != s.want {
			t.Fatalf("%s (t+%s): action = %d, want %d", s.name, s.offset, got, s.want)
		}
		// This table models pages that always reach the sink.
		if got == dmPage || got == dmRepage {
			d.pageDelivered(at(s.offset))
		}
	}
}

// TestDeadmanFailedPageRetriesNextTick pins the delivery-failure contract: a
// page only starts the hourly repage cadence once it actually reached the
// sink, so a failed page is retried on the next poll tick.
func TestDeadmanFailedPageRetriesNextTick(t *testing.T) {
	start := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	d := newDeadman(start, 10*time.Minute, time.Hour)
	at := func(offset time.Duration) time.Time { return start.Add(offset) }

	steps := []struct {
		name      string
		offset    time.Duration
		active    bool
		want      deadmanAction
		delivered bool // whether the page attempt for this step succeeds
	}{
		{"silence crosses timeout pages", 12 * time.Minute, false, dmPage, false},
		{"failed page retries on next tick", 13 * time.Minute, false, dmRepage, false},
		{"still failing keeps retrying", 14 * time.Minute, false, dmRepage, true},
		{"delivered page quiets the next tick", 15 * time.Minute, false, dmNone, false},
		{"hourly cadence counts from the delivered page", 73 * time.Minute, false, dmNone, false},
		{"repage an hour after the delivered page", 74 * time.Minute, false, dmRepage, false},
		{"failed repage also retries on next tick", 75 * time.Minute, false, dmRepage, true},
		{"quiet again after the repage lands", 76 * time.Minute, false, dmNone, false},
	}
	for _, s := range steps {
		got := d.observe(at(s.offset), s.active)
		if got != s.want {
			t.Fatalf("%s (t+%s): action = %d, want %d", s.name, s.offset, got, s.want)
		}
		if s.delivered {
			d.pageDelivered(at(s.offset))
		}
	}
}

func TestEventName(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"event field wins over attachment title", `{"attachments":[{"title":"some title","fields":[{"title":"event","value":"DiskFull"}]}]}`, "DiskFull"},
		{"alertname field", `{"attachments":[{"fields":[{"title":"alertname","value":"KubePodCrashLooping"}]}]}`, "KubePodCrashLooping"},
		{"attachment title fallback", `{"attachments":[{"title":"canary failed"}]}`, "canary failed"},
		{"flagger author_name fallback", flaggerPayload, "keycloak.guardian-iam-beta"},
		{"alerta text-only summary parses the event", alertaPayload, "KubePodCrashLooping"},
		{"text first line fallback", `{"text":"first line\nsecond line"}`, "first line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventName(mustParse(t, tc.payload)); got != tc.want {
				t.Errorf("eventName = %q, want %q", got, tc.want)
			}
		})
	}
}

func mustParse(t *testing.T, s string) slackPayload {
	t.Helper()
	var p slackPayload
	if err := json.Unmarshal([]byte(s), &p); err != nil {
		t.Fatalf("fixture does not parse: %v", err)
	}
	return p
}

// amReplica serves the /api/v2/alerts endpoint of one fake Alertmanager
// replica: watchdog=true answers with an active Watchdog, false with an
// empty alert list — the answer of a healthy replica that vmalert's
// notifier connection is not pinned to.
func amReplica(t *testing.T, watchdog bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/alerts" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if watchdog {
			_, _ = w.Write([]byte(`[{"labels":{"alertname":"Watchdog"},"status":{"state":"active"}}]`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWatchdogActiveAnyReplica(t *testing.T) {
	empty := amReplica(t, false)
	active := amReplica(t, true)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)

	s := newTestServer(&fakeSink{})
	s.client = &http.Client{Timeout: 2 * time.Second}
	ctx := context.Background()

	cases := []struct {
		name    string
		urls    []string
		want    bool
		wantErr bool
	}{
		{"one replica holds it", []string{empty.URL, active.URL}, true, false},
		{"no replica holds it", []string{empty.URL, empty.URL}, false, false},
		{"holder unreachable counts as not seen", []string{empty.URL, down.URL}, false, false},
		{"all replicas unreachable is an error", []string{down.URL, down.URL}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.watchdogActiveAny(ctx, tc.urls)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("watchdogActiveAny = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReplicaURLs(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		url    string
		lookup func(context.Context, string) ([]string, error)
		want   []string
	}{
		{
			"headless service expands to every pod",
			"http://am.tenant-root.svc:9093",
			func(_ context.Context, host string) ([]string, error) {
				if host != "am.tenant-root.svc" {
					return nil, errors.New("unexpected host " + host)
				}
				return []string{"10.244.0.183", "10.244.0.185", "fd00::1"}, nil
			},
			[]string{"http://10.244.0.183:9093", "http://10.244.0.185:9093", "http://[fd00::1]:9093"},
		},
		{
			"resolution failure falls back to the configured URL",
			"http://am.tenant-root.svc:9093",
			func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") },
			[]string{"http://am.tenant-root.svc:9093"},
		},
		{
			"nil resolver falls back to the configured URL",
			"http://am.tenant-root.svc:9093",
			nil,
			[]string{"http://am.tenant-root.svc:9093"},
		},
		{
			"portless URL keeps addresses bare",
			"http://am.tenant-root.svc",
			func(context.Context, string) ([]string, error) {
				return []string{"10.244.0.183", "fd00::1"}, nil
			},
			[]string{"http://10.244.0.183", "http://[fd00::1]"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestServer(&fakeSink{})
			s.cfg.alertmanagerURL = tc.url
			s.lookupHost = tc.lookup
			got, err := s.replicaURLs(ctx)
			if err != nil {
				t.Fatalf("replicaURLs: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("replicaURLs = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("url[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// A blackholed replica must not shadow the one holding the Watchdog: the
// headless Service publishes not-ready addresses, so a dead pod's IP stays
// in DNS and the poll has to reach past it within the same budget.
func TestWatchdogActiveAnyHungReplicaDoesNotShadowHolder(t *testing.T) {
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hung.Close)
	active := amReplica(t, true)

	s := newTestServer(&fakeSink{})
	s.client = &http.Client{Timeout: 5 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	got, err := s.watchdogActiveAny(ctx, []string{hung.URL, active.URL})
	if err != nil {
		t.Fatalf("watchdogActiveAny: %v", err)
	}
	if !got {
		t.Fatal("watchdogActiveAny = false, want true: hung replica shadowed the holder")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("answer took %v, want well under the poll budget", elapsed)
	}
}

// End-to-end wiring: watchdogActive resolves the configured host and reaches
// the replica through the rebuilt per-address URL.
func TestWatchdogActiveResolvesReplicas(t *testing.T) {
	active := amReplica(t, true)
	u, err := url.Parse(active.URL)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(&fakeSink{})
	s.client = &http.Client{Timeout: 2 * time.Second}
	s.cfg.alertmanagerURL = "http://alertmanager.example.internal:" + u.Port()
	s.lookupHost = func(_ context.Context, host string) ([]string, error) {
		if host != "alertmanager.example.internal" {
			return nil, errors.New("unexpected host " + host)
		}
		return []string{u.Hostname()}, nil
	}
	got, err := s.watchdogActive(context.Background())
	if err != nil {
		t.Fatalf("watchdogActive: %v", err)
	}
	if !got {
		t.Fatal("watchdogActive = false, want true")
	}
}

// recordingSink captures every delivered notification.
type recordingSink struct {
	mu    sync.Mutex
	sent  []notification
	fail  error
	delay time.Duration
}

func (r *recordingSink) deliver(_ context.Context, n notification) error {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.sent = append(r.sent, n)
	return nil
}

func (r *recordingSink) snapshot() []notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notification(nil), r.sent...)
}

func TestPacedSinkCoalescesStorm(t *testing.T) {
	inner := &recordingSink{delay: 20 * time.Millisecond}
	var coalesced atomic.Uint64
	paced := newPacedSink(inner, 30*time.Millisecond, &coalesced)

	// One leader to occupy the sender, then a burst that must merge.
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n := notification{Title: fmt.Sprintf("alert-%d", i), Priority: 3 + i%3}
			if err := paced.deliver(context.Background(), n); err != nil {
				t.Errorf("deliver %d: %v", i, err)
			}
		}(i)
		if i == 0 {
			time.Sleep(5 * time.Millisecond) // let the leader start sending
		}
	}
	wg.Wait()

	sent := inner.snapshot()
	if len(sent) >= 6 {
		t.Fatalf("expected coalescing, got %d individual sends", len(sent))
	}
	if coalesced.Load() == 0 {
		t.Fatal("expected coalesced counter to advance")
	}
	foundDigest := false
	for _, n := range sent {
		if strings.Contains(n.Title, "storm coalesced") {
			foundDigest = true
			if n.Priority < 4 {
				t.Fatalf("digest must carry the max member priority, got %d", n.Priority)
			}
		}
	}
	if !foundDigest {
		t.Fatalf("no digest among %d sends: %+v", len(sent), sent)
	}
}

func TestPacedSinkPacesSends(t *testing.T) {
	inner := &recordingSink{}
	var coalesced atomic.Uint64
	paced := newPacedSink(inner, 60*time.Millisecond, &coalesced)

	start := time.Now()
	if err := paced.deliver(context.Background(), notification{Title: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := paced.deliver(context.Background(), notification{Title: "b"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("second send not paced: %v elapsed", elapsed)
	}
	if got := len(inner.snapshot()); got != 2 {
		t.Fatalf("want 2 sends, got %d", got)
	}
}

func TestNtfySinkHonorsRetryAfter(t *testing.T) {
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	var slept []time.Duration
	s := &ntfySink{
		url:    up.URL,
		client: up.Client(),
		sleep:  func(_ context.Context, d time.Duration) { slept = append(slept, d) },
	}
	if err := s.deliver(context.Background(), notification{Title: "t"}); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Fatalf("want one 7s Retry-After sleep, got %v", slept)
	}
}

func TestParseRetryAfterClamps(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"7":    7 * time.Second,
		"1":    5 * time.Second,
		"9999": 5 * time.Minute,
		"":     30 * time.Second,
		"soon": 30 * time.Second,
	} {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
