package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// alertaPayload mirrors what Alerta's slack plugin posts: text summary plus
// one attachment with a color and event/resource/severity/environment fields.
const alertaPayload = `{
	"text": "*[Production] CRITICAL PodCrashLooping on tenant-root/openbao-0*",
	"attachments": [{
		"color": "danger",
		"title": "PodCrashLooping on tenant-root/openbao-0",
		"text": "container keeps restarting",
		"fields": [
			{"title": "event", "value": "PodCrashLooping", "short": true},
			{"title": "resource", "value": "tenant-root/openbao-0", "short": true},
			{"title": "severity", "value": "critical", "short": true},
			{"title": "environment", "value": "Production", "short": true}
		],
		"mrkdwn_in": ["text"]
	}]
}`

// flaggerPayload mirrors a Flagger AlertProvider slack message: no attachment
// title, no severity field, color carries the outcome.
const flaggerPayload = `{
	"channel": "",
	"username": "flagger",
	"text": "keycloak-passthrough.guardian-iam-beta",
	"attachments": [{
		"color": "danger",
		"author_name": "flagger",
		"text": "Canary analysis failed, rolling back.",
		"fields": [
			{"title": "Target", "value": "Deployment/keycloak", "short": true},
			{"title": "Failed checks threshold", "value": "2", "short": true}
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
			name:         "alerta style",
			payload:      alertaPayload,
			wantTitle:    "PodCrashLooping on tenant-root/openbao-0",
			wantPriority: 5,
			wantTags:     []string{"critical", "production"},
			wantInBody:   []string{"container keeps restarting", "event: PodCrashLooping", "resource: tenant-root/openbao-0"},
		},
		{
			name:         "flagger style falls back to first line of text",
			payload:      flaggerPayload,
			wantTitle:    "keycloak-passthrough.guardian-iam-beta",
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

func TestPriorityFor(t *testing.T) {
	cases := []struct {
		severity, color string
		want            int
	}{
		{"critical", "", 5},
		{"", "danger", 5},
		{"major", "", 4},
		{"warning", "", 3},
		{"minor", "", 2},
		{"informational", "", 2},
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
