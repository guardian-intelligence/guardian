// alert-relay — the pluggable delivery seam of the alerting pipeline:
// Slack-incoming-webhook JSON in (the de-facto payload format both Alerta's
// slack plugin and Flagger AlertProviders emit), ntfy out. The sink sits
// behind an interface and is chosen by config, so swapping ntfy for another
// pager is a config change, not a payload-format migration.
//
// It also runs the pipeline's dead-man's switch: vmalert evaluates an
// always-firing Watchdog rule, the relay polls Alertmanager for it, and a
// missing Watchdog becomes a page — silence anywhere in the
// scrape→vmalert→alertmanager path pages instead of staying quiet. The poll
// fans out to every replica behind the Alertmanager Service: gossip
// replicates silences and the notification log but NOT alert state, and
// vmalert's notifier delivers each alert to one replica per keep-alive
// connection, so the Watchdog is only guaranteed to exist on some replica.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"log/slog"

	// The @ubuntu_noble_base image ships no ca-certificates bundle, so the
	// system cert pool is empty and every public-TLS dial (ntfy) fails x509
	// verification. This blank import embeds the Go team's Mozilla root
	// bundle, used only when the system pool is empty — the bundle versions
	// via go.mod like any other dependency.
	_ "golang.org/x/crypto/x509roots/fallback"

	// The image also ships no tzdata, and the pager renders wall-clock
	// times in the on-call's zone (renderTimeTokens): without the embedded
	// zone database LoadLocation fails and every page silently degrades to
	// UTC.
	_ "time/tzdata"
)

// slackPayload is the subset of the Slack incoming-webhook format the relay
// reads; unknown fields are ignored by encoding/json, which is the whole
// tolerance strategy.
type slackPayload struct {
	Text        string            `json:"text"`
	Attachments []slackAttachment `json:"attachments"`
}

type slackAttachment struct {
	Color      string       `json:"color"`
	Title      string       `json:"title"`
	AuthorName string       `json:"author_name"`
	Text       string       `json:"text"`
	Fields     []slackField `json:"fields"`
}

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

// notification is the sink-side shape: ntfy's title/priority/tags/body model,
// which any pager sink can be mapped from.
type notification struct {
	Title    string
	Priority int
	Tags     []string
	Body     string
}

type sink interface {
	deliver(ctx context.Context, n notification) error
}

// priorityMap covers both Alerta severities and Slack attachment colors
// (danger/good are colors, the rest severities): critical→5, major→4,
// minor/warning→3, informational/indeterminate→2, ok/cleared/normal→2.
// Unmapped values page at the default priority 3 rather than being dropped
// or demoted.
var priorityMap = map[string]int{
	"critical":      5,
	"danger":        5,
	"major":         4,
	"minor":         3,
	"warning":       3,
	"informational": 2,
	"indeterminate": 2,
	"ok":            2,
	"cleared":       2,
	"normal":        2,
	"good":          2,
}

func priorityFor(severity, color string) int {
	if p, ok := priorityMap[strings.ToLower(strings.TrimSpace(severity))]; ok {
		return p
	}
	if p, ok := priorityMap[strings.ToLower(strings.TrimSpace(color))]; ok {
		return p
	}
	return 3
}

// fieldValue returns the first field value whose title matches any of the
// given names, case-insensitively, across all attachments.
func fieldValue(p slackPayload, names ...string) string {
	for _, a := range p.Attachments {
		for _, f := range a.Fields {
			for _, name := range names {
				if strings.EqualFold(strings.TrimSpace(f.Title), name) && f.Value != "" {
					return f.Value
				}
			}
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// alertaSummary holds the fields recovered from the text-only summary line
// posted by Alerta's slack plugin.
type alertaSummary struct {
	status      string
	environment string
	service     string
	severity    string
	event       string
	resource    string
	// dashboardLink is the raw URL from the trailing <url|short_id> segment;
	// empty when the summary carried no link.
	dashboardLink string
}

// The primary producer — Alerta's slack plugin (alerta-contrib
// plugins/slack/alerta_slack.py) as rendered by the Cozystack v1.5.0 chart,
// where only SLACK_WEBHOOK_URL is set and SLACK_ATTACHMENTS defaults to
// False — posts TEXT-ONLY payloads: {"username", "channel", "text"} with no
// attachments. The text is SLACK_DEFAULT_SUMMARY_FMT, which is the parsing
// contract for this regex:
//
//	'*[{status}] {environment} {service} {severity}* - _{event} on {resource}_ <{dashboard}/#/alert/{alert_id}|{short_id}>'
//
// with status and severity .capitalize()d, environment .upper()ed, service
// ','.join()ed (individual service names may contain spaces), and
// event/resource raw. Environment and severity are single tokens, so the
// lazy middle group absorbs the service list (including an empty one, which
// renders as two adjacent spaces). The dashboard link is left optional only
// for tolerance: DASHBOARD_URL defaults to the empty string but the
// '<...|...>' wrapper is always emitted.
//
// The italic segment is split at the LAST " on " — the greedy (?P<event>.+)
// encodes that choice — because the event itself can contain " on " (e.g.
// "FailedMount on startup") while resources here are namespace/pod-style
// paths that never do.
var alertaSummaryRe = regexp.MustCompile(`^\*\[(?P<status>[^\]]+)\] +(?P<environment>\S+) +(?P<service>.*?) +(?P<severity>\S+)\* - _(?P<event>.+) on (?P<resource>.+?)_(?: <(?P<link>[^>|]*)(?:\|[^>]*)?>)?$`)

// parseAlertaSummary attempts to recover the Alerta alert fields from the
// first line of a text-only payload. A non-match reports ok=false and the
// caller falls back to the generic composition — a page is never dropped
// over a format drift, it just loses severity/tags.
func parseAlertaSummary(text string) (alertaSummary, bool) {
	m := alertaSummaryRe.FindStringSubmatch(firstLine(text))
	if m == nil {
		return alertaSummary{}, false
	}
	group := func(name string) string {
		return strings.TrimSpace(m[alertaSummaryRe.SubexpIndex(name)])
	}
	return alertaSummary{
		status:        group("status"),
		environment:   group("environment"),
		service:       group("service"),
		severity:      group("severity"),
		event:         group("event"),
		resource:      group("resource"),
		dashboardLink: group("link"),
	}, true
}

// alertID extracts the Alerta alert id from the dashboard link's /#/alert/<id>
// fragment. The id is only accepted in UUID shape: it is interpolated into a
// request path against the Alerta API, and anything else in that position is
// a payload playing games, not an alert.
var alertaIDRe = regexp.MustCompile(`/#/alert/([0-9a-fA-F-]{8,64})$`)

func (s alertaSummary) alertID() string {
	m := alertaIDRe.FindStringSubmatch(s.dashboardLink)
	if m == nil {
		return ""
	}
	return m[1]
}

// notification maps a parsed summary onto the sink model through the same
// machinery attachment payloads use: severity drives priorityFor, tags carry
// severity + environment, and the title restores Alerta's "{event} on
// {resource}" identity — prefixed with the status when the alert is no
// longer open, so a Closed/Ack page reads as one at a glance.
func (s alertaSummary) notification(fullText string) notification {
	title := s.event + " on " + s.resource
	if !strings.EqualFold(s.status, "open") {
		title = "[" + s.status + "] " + title
	}
	return notification{
		Title:    title,
		Priority: priorityFor(s.severity, ""),
		Tags:     []string{strings.ToLower(s.severity), strings.ToLower(s.environment)},
		Body:     strings.TrimSpace(fullText),
	}
}

// eventName resolves the alert's identity for suppression and logging:
// Alerta's text-only summaries carry it in the parsed event, fielded
// payloads in an "event" (Alerta) or "alertname" (Alertmanager) field,
// Flagger in the attachment author_name ("{workload}.{namespace}").
func eventName(p slackPayload) string {
	if v := fieldValue(p, "event", "alertname"); v != "" {
		return v
	}
	for _, a := range p.Attachments {
		if t := strings.TrimSpace(a.Title); t != "" {
			return t
		}
	}
	for _, a := range p.Attachments {
		if an := strings.TrimSpace(a.AuthorName); an != "" {
			return an
		}
	}
	if s, ok := parseAlertaSummary(p.Text); ok {
		return s.event
	}
	return firstLine(p.Text)
}

// isHeartbeat reports whether the payload is the always-firing liveness
// signal (Watchdog/Heartbeat): counted, never forwarded — the dead-man
// poller is what turns its absence into a page.
func isHeartbeat(p slackPayload) bool {
	ev := eventName(p)
	return strings.EqualFold(ev, "Watchdog") || strings.EqualFold(ev, "Heartbeat")
}

func compose(p slackPayload) notification {
	// When no attachment carries severity or event fields, this is not an
	// attachment-style payload: try the Alerta text-only summary before the
	// generic fallbacks, so severity reaches priorityFor instead of every
	// Alerta page landing at the default priority with no tags.
	if fieldValue(p, "severity") == "" && fieldValue(p, "event", "alertname") == "" {
		if s, ok := parseAlertaSummary(p.Text); ok {
			return s.notification(p.Text)
		}
	}

	title := ""
	for _, a := range p.Attachments {
		if t := strings.TrimSpace(a.Title); t != "" {
			title = t
			break
		}
	}
	if title == "" {
		if ev := fieldValue(p, "event", "alertname"); ev != "" {
			title = strings.TrimSpace(ev + " " + fieldValue(p, "resource"))
		}
	}
	if title == "" {
		for _, a := range p.Attachments {
			if an := strings.TrimSpace(a.AuthorName); an != "" {
				title = an
				break
			}
		}
	}
	if title == "" {
		title = firstLine(p.Text)
	}
	if title == "" {
		title = "alert"
	}

	var lines []string
	if t := strings.TrimSpace(p.Text); t != "" {
		lines = append(lines, t)
	}
	color := ""
	for _, a := range p.Attachments {
		if color == "" {
			color = a.Color
		}
		if t := strings.TrimSpace(a.Text); t != "" {
			lines = append(lines, t)
		}
		for _, f := range a.Fields {
			if f.Title == "" && f.Value == "" {
				continue
			}
			lines = append(lines, f.Title+": "+f.Value)
		}
	}

	var tags []string
	if sev := fieldValue(p, "severity"); sev != "" {
		tags = append(tags, strings.ToLower(sev))
	}
	if env := fieldValue(p, "environment"); env != "" {
		tags = append(tags, strings.ToLower(env))
	}

	body := strings.Join(lines, "\n")
	if body == "" {
		body = title
	}
	return notification{
		Title:    title,
		Priority: priorityFor(fieldValue(p, "severity"), color),
		Tags:     tags,
		Body:     body,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ptTokenRe matches the @pt:<unix-seconds>@ tokens that rule annotations
// embed via {{ $activeAt.Unix }}. vmalert's Go templates cannot construct a
// *time.Location, so rules ship machine time and the relay — the one hop
// with a zone database — owns turning it into the on-call's wall clock.
var ptTokenRe = regexp.MustCompile(`@pt:(\d{1,19})@`)

// renderTimeTokens replaces every @pt:<unix>@ token with the moment
// formatted in loc (e.g. "7:06:21 PM PDT"). Unparseable tokens pass through
// verbatim — a mangled annotation must never cost the page its text.
func renderTimeTokens(s string, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	return ptTokenRe.ReplaceAllStringFunc(s, func(m string) string {
		secs, err := strconv.ParseInt(m[len("@pt:"):len(m)-1], 10, 64)
		if err != nil {
			return m
		}
		return time.Unix(secs, 0).In(loc).Format("3:04:05 PM MST")
	})
}

// pagerLocation resolves the on-call's zone once at startup; a failed load
// degrades every timestamp to UTC rather than degrading delivery.
func pagerLocation() *time.Location {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		slog.Error("pager timezone unavailable, timestamps render UTC", "err", err)
		return time.UTC
	}
	return loc
}

// enrichFromAlerta fetches the alert's text attribute — where Alerta stores
// the rule's description/summary annotation, which the slack plugin's
// summary line drops — and leads the page body with it, so the page reads
// as a sentence instead of an alertname. Strictly best-effort: any failure
// is counted, logged, and the page goes out exactly as it would have.
func (s *server) enrichFromAlerta(ctx context.Context, text string, n notification) notification {
	sum, ok := parseAlertaSummary(text)
	if !ok {
		return n
	}
	id := sum.alertID()
	if id == "" {
		return n
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.alertaURL+"/api/alert/"+id, nil)
	if err != nil {
		// The only way here is a malformed ALERTA_URL: a config typo must
		// not silently disable enrichment forever.
		s.m.enrichFailures.Add(1)
		slog.Warn("alerta enrichment request build failed", "err", err)
		return n
	}
	if s.cfg.alertaAPIKey != "" {
		req.Header.Set("Authorization", "Key "+s.cfg.alertaAPIKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.m.enrichFailures.Add(1)
		slog.Warn("alerta enrichment fetch failed", "err", err)
		return n
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		s.m.enrichFailures.Add(1)
		slog.Warn("alerta enrichment fetch failed", "status", resp.StatusCode)
		return n
	}
	var payload struct {
		Alert struct {
			Text string `json:"text"`
		} `json:"alert"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		s.m.enrichFailures.Add(1)
		slog.Warn("alerta enrichment decode failed", "err", err)
		return n
	}
	alertText := strings.TrimSpace(payload.Alert.Text)
	if alertText == "" || strings.Contains(n.Body, alertText) {
		return n
	}
	n.Body = truncateUTF8(alertText, 2048) + "\n" + n.Body
	return n
}

// truncateUTF8 cuts at no more than n bytes without splitting a rune —
// byte-slicing an annotation mid-rune would ship invalid UTF-8 at the seam
// of every page body that hits the cap.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

type metrics struct {
	forwarded         atomic.Uint64
	forwardFailures   atomic.Uint64
	selfAlertFailures atomic.Uint64
	suppressed        atomic.Uint64
	coalesced         atomic.Uint64
	enrichFailures    atomic.Uint64
	watchdogLastSeen  atomic.Int64
	pipelineSilent    atomic.Int64
}

func (m *metrics) render(w io.Writer) {
	fmt.Fprintf(w, "# HELP relay_forwarded_total Alerts forwarded to the sink.\n")
	fmt.Fprintf(w, "# TYPE relay_forwarded_total counter\n")
	fmt.Fprintf(w, "relay_forwarded_total %d\n", m.forwarded.Load())
	fmt.Fprintf(w, "# HELP relay_forward_failures_total Alerts that failed delivery after all retries.\n")
	fmt.Fprintf(w, "# TYPE relay_forward_failures_total counter\n")
	fmt.Fprintf(w, "relay_forward_failures_total %d\n", m.forwardFailures.Load())
	fmt.Fprintf(w, "# HELP relay_self_alert_forward_failures_total AlertRelayForwardFailures notifications that failed delivery after all retries.\n")
	fmt.Fprintf(w, "# TYPE relay_self_alert_forward_failures_total counter\n")
	fmt.Fprintf(w, "relay_self_alert_forward_failures_total %d\n", m.selfAlertFailures.Load())
	fmt.Fprintf(w, "# HELP relay_heartbeats_suppressed_total Watchdog/Heartbeat payloads counted but not forwarded.\n")
	fmt.Fprintf(w, "# TYPE relay_heartbeats_suppressed_total counter\n")
	fmt.Fprintf(w, "relay_heartbeats_suppressed_total %d\n", m.suppressed.Load())
	fmt.Fprintf(w, "# HELP relay_notifications_coalesced_total Notifications merged into a digest instead of sent individually.\n")
	fmt.Fprintf(w, "# TYPE relay_notifications_coalesced_total counter\n")
	fmt.Fprintf(w, "relay_notifications_coalesced_total %d\n", m.coalesced.Load())
	fmt.Fprintf(w, "# HELP relay_alerta_enrichment_failures_total Pages delivered without Alerta text because the enrichment fetch failed.\n")
	fmt.Fprintf(w, "# TYPE relay_alerta_enrichment_failures_total counter\n")
	fmt.Fprintf(w, "relay_alerta_enrichment_failures_total %d\n", m.enrichFailures.Load())
	fmt.Fprintf(w, "# HELP relay_watchdog_last_seen_timestamp_seconds Unix time an active Watchdog was last observed in Alertmanager.\n")
	fmt.Fprintf(w, "# TYPE relay_watchdog_last_seen_timestamp_seconds gauge\n")
	fmt.Fprintf(w, "relay_watchdog_last_seen_timestamp_seconds %d\n", m.watchdogLastSeen.Load())
	fmt.Fprintf(w, "# HELP relay_pipeline_silent 1 while the dead-man considers the alerting pipeline broken.\n")
	fmt.Fprintf(w, "# TYPE relay_pipeline_silent gauge\n")
	fmt.Fprintf(w, "relay_pipeline_silent %d\n", m.pipelineSilent.Load())
}

// ntfySink delivers to a single ntfy topic URL. The URL is a low-grade
// credential (topic name is the secret): it must never appear in logs.
type ntfySink struct {
	url    string
	token  string
	client *http.Client
	sleep  func(context.Context, time.Duration)
}

// rateLimitedError carries the sink's Retry-After so the retry loop paces
// itself to the server's clock instead of hammering through its own
// backoff — retrying straight into a 429 is how the 2026-07-07 alert storm
// escalated a rate limit into an IP-level ban.
type rateLimitedError struct{ retryAfter time.Duration }

func (e rateLimitedError) Error() string { return "ntfy responded 429" }

// parseRetryAfter reads the delay-seconds form of Retry-After, clamped to
// [5s, 5m]; anything unparseable gets a conservative 30s.
func parseRetryAfter(v string) time.Duration {
	secs, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || secs <= 0 {
		return 30 * time.Second
	}
	d := time.Duration(secs) * time.Second
	if d < 5*time.Second {
		d = 5 * time.Second
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

func (s *ntfySink) deliver(ctx context.Context, n notification) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt*2) * time.Second
			var rl rateLimitedError
			if errors.As(lastErr, &rl) {
				delay = rl.retryAfter
			}
			s.sleep(ctx, delay)
			if ctx.Err() != nil {
				return lastErr
			}
		}
		if lastErr = s.post(ctx, n); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

// sleepCtx sleeps for d but wakes early when ctx is done, so backoff never
// outlives the delivery deadline.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// sanitizeHeader makes an alert-derived value safe as an HTTP header value:
// header values must be single-line, and Go's transport rejects the whole
// request over any control byte — which would deterministically fail all
// retries and lose the page. Every byte below 0x20 (CR/LF included) and
// 0x7F (DEL) becomes a space.
func sanitizeHeader(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

func (s *ntfySink) post(ctx context.Context, n notification) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(n.Body))
	if err != nil {
		// The topic URL is a credential: never let it escape in an error.
		return errors.New("ntfy request build failed")
	}
	req.Header.Set("X-Title", sanitizeHeader(n.Title))
	req.Header.Set("X-Priority", strconv.Itoa(n.Priority))
	if len(n.Tags) > 0 {
		tags := make([]string, len(n.Tags))
		for i, tag := range n.Tags {
			tags[i] = sanitizeHeader(tag)
		}
		req.Header.Set("X-Tags", strings.Join(tags, ","))
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		// *url.Error embeds the full topic URL (a credential); surface only
		// the transport cause.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return fmt.Errorf("ntfy post: %w", uerr.Err)
		}
		return errors.New("ntfy post failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusTooManyRequests {
		return rateLimitedError{retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy responded %d", resp.StatusCode)
	}
	return nil
}

// pacedSink serializes deliveries through a single sender paced to the
// sink's public rate limits, coalescing whatever arrives while the sender
// is busy or cooling down into one digest notification. This is what
// stands between an alert storm and the pager's rate limiter: on
// 2026-07-07 a metric-lag storm of absence alerts retried its way into an
// IP-level ntfy.sh ban, and the pager was dark exactly when it was needed
// (including for its own AlertRelayForwardFailures).
type pacedSink struct {
	inner       sink
	minInterval time.Duration
	now         func() time.Time
	sleep       func(context.Context, time.Duration)
	coalesced   *atomic.Uint64

	mu       sync.Mutex
	pending  []notification
	waiters  []chan error
	sending  bool
	lastSend time.Time
}

func newPacedSink(inner sink, minInterval time.Duration, coalesced *atomic.Uint64) *pacedSink {
	return &pacedSink{
		inner:       inner,
		minInterval: minInterval,
		now:         time.Now,
		sleep:       sleepCtx,
		coalesced:   coalesced,
	}
}

func (s *pacedSink) deliver(ctx context.Context, n notification) error {
	done := make(chan error, 1)
	s.mu.Lock()
	s.pending = append(s.pending, n)
	s.waiters = append(s.waiters, done)
	if !s.sending {
		s.sending = true
		go s.run()
	}
	s.mu.Unlock()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		// The buffered channel lets the sender resolve this batch later
		// without blocking; the caller just stops waiting for the outcome.
		return ctx.Err()
	}
}

func (s *pacedSink) run() {
	for {
		s.mu.Lock()
		if len(s.pending) == 0 {
			s.sending = false
			s.mu.Unlock()
			return
		}
		if wait := s.minInterval - s.now().Sub(s.lastSend); wait > 0 {
			s.mu.Unlock()
			s.sleep(context.Background(), wait)
			continue
		}
		batch := s.pending
		waiters := s.waiters
		s.pending, s.waiters = nil, nil
		s.lastSend = s.now()
		s.mu.Unlock()

		n := batch[0]
		if len(batch) > 1 {
			n = digest(batch)
			s.coalesced.Add(uint64(len(batch) - 1))
		}
		dctx, cancel := context.WithTimeout(context.Background(), deliveryBudget)
		err := s.inner.deliver(dctx, n)
		cancel()
		for _, w := range waiters {
			w <- err
		}
	}
}

// digest merges a storm batch into one page: highest member priority, one
// title line per alert, union of tags.
func digest(batch []notification) notification {
	var b strings.Builder
	maxPriority := 0
	seen := map[string]struct{}{}
	var tags []string
	for _, n := range batch {
		if n.Priority > maxPriority {
			maxPriority = n.Priority
		}
		fmt.Fprintf(&b, "- %s\n", firstLine(n.Title))
		for _, t := range n.Tags {
			if _, ok := seen[t]; !ok {
				seen[t] = struct{}{}
				tags = append(tags, t)
			}
		}
	}
	return notification{
		Title:    fmt.Sprintf("%d alerts (storm coalesced)", len(batch)),
		Priority: maxPriority,
		Tags:     tags,
		Body:     truncate(b.String(), 4096),
	}
}

type deadmanAction int

const (
	dmNone deadmanAction = iota
	dmPage
	dmRepage
	dmRecover
)

// deadman is the state machine behind the pipeline dead-man's switch. It is
// clock-free: callers pass now, which is what makes it testable.
type deadman struct {
	timeout     time.Duration
	repageEvery time.Duration
	lastSeen    time.Time
	// lastPage is the time of the last page that actually reached the sink
	// (recorded via pageDelivered, not by observe): a failed page leaves
	// pagePending set so the next poll tick retries instead of waiting out
	// the repage window.
	lastPage    time.Time
	pagePending bool
	silent      bool
}

func newDeadman(start time.Time, timeout, repageEvery time.Duration) *deadman {
	return &deadman{timeout: timeout, repageEvery: repageEvery, lastSeen: start}
}

func (d *deadman) observe(now time.Time, watchdogActive bool) deadmanAction {
	if watchdogActive {
		d.lastSeen = now
		if d.silent {
			d.silent = false
			d.pagePending = false
			return dmRecover
		}
		return dmNone
	}
	if now.Sub(d.lastSeen) < d.timeout {
		return dmNone
	}
	if !d.silent {
		d.silent = true
		d.pagePending = true
		return dmPage
	}
	if d.pagePending || now.Sub(d.lastPage) >= d.repageEvery {
		d.pagePending = true
		return dmRepage
	}
	return dmNone
}

// pageDelivered records a successful page. Only delivered pages start the
// hourly repage cadence; until one lands, observe keeps returning dmRepage
// every tick.
func (d *deadman) pageDelivered(now time.Time) {
	d.lastPage = now
	d.pagePending = false
}

type config struct {
	ntfyURL         string
	ntfyToken       string
	alertmanagerURL string
	// alertaURL enables page-text enrichment (enrichFromAlerta) when set;
	// empty leaves pages exactly as the slack plugin rendered them.
	alertaURL       string
	alertaAPIKey    string
	watchdogTimeout time.Duration
	listenAddr      string
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func loadConfig() (config, error) {
	cfg := config{
		ntfyURL:         os.Getenv("NTFY_URL"),
		ntfyToken:       os.Getenv("NTFY_TOKEN"),
		alertmanagerURL: envOr("ALERTMANAGER_URL", "http://vmalertmanager-alertmanager.tenant-root.svc:9093"),
		alertaURL:       strings.TrimSuffix(os.Getenv("ALERTA_URL"), "/"),
		alertaAPIKey:    os.Getenv("ALERTA_API_KEY"),
		listenAddr:      envOr("LISTEN_ADDR", ":8080"),
	}
	if cfg.ntfyURL == "" {
		return cfg, errors.New("NTFY_URL is required")
	}
	timeout, err := time.ParseDuration(envOr("WATCHDOG_TIMEOUT", "10m"))
	if err != nil {
		return cfg, fmt.Errorf("WATCHDOG_TIMEOUT: %w", err)
	}
	cfg.watchdogTimeout = timeout
	return cfg, nil
}

// deliveryBudget bounds one full sink delivery: 3 attempts at up to 10s each
// plus 2s+4s of backoff (~36s worst case), with headroom.
const deliveryBudget = 45 * time.Second

type server struct {
	cfg    config
	m      *metrics
	out    sink
	loc    *time.Location
	client *http.Client
	// lookupHost resolves the Alertmanager hostname to per-replica
	// addresses for the dead-man poll; injectable for tests.
	lookupHost func(ctx context.Context, host string) ([]string, error)
	// inflight tracks detached delivery goroutines so graceful shutdown can
	// wait for accepted alerts to finish delivering.
	inflight sync.WaitGroup
}

func (s *server) handleSlack(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	var p slackPayload
	parseErr := json.Unmarshal(raw, &p)
	parsed := parseErr == nil && (p.Text != "" || len(p.Attachments) > 0)

	var n notification
	switch {
	case !parsed:
		// Never drop an alert silently: an unparseable payload still pages,
		// carrying the raw body (bounded) so the on-call can read it.
		n = notification{Title: "unparsed alert", Priority: 3, Body: truncate(string(raw), 4096)}
		slog.Warn("unparsed alert payload forwarded", "bytes", len(raw))
	case isHeartbeat(p):
		s.m.suppressed.Add(1)
		slog.Info("heartbeat suppressed", "event", eventName(p))
		w.WriteHeader(http.StatusOK)
		return
	default:
		n = compose(p)
	}

	// Delivery runs in a tracked goroutine under a context detached from the
	// producer's: Flagger's alert client hangs up after a hard 5s with no
	// retries, and its disconnect must not cancel the remaining sink
	// attempts. The producer gets 202 as soon as the payload is accepted
	// (delivery outcome is visible via relay_forward_failures_total), and
	// graceful shutdown waits on inflight so an accepted alert is never
	// abandoned.
	ev := eventName(p)
	s.inflight.Add(1)
	go func() {
		defer s.inflight.Done()
		dctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), deliveryBudget)
		defer cancel()
		if parsed {
			if s.cfg.alertaURL != "" {
				// Detached from dctx: enrichment's 5s bound must come out
				// of its own budget, not the delivery headroom the
				// deliveryBudget comment accounts for.
				n = s.enrichFromAlerta(context.WithoutCancel(dctx), p.Text, n)
			}
			// Unparsed payloads are forwarded verbatim as forensic
			// evidence; only composed pages get token rendering.
			n.Title = renderTimeTokens(n.Title, s.loc)
			n.Body = renderTimeTokens(n.Body, s.loc)
		}
		if err := s.out.deliver(dctx, n); err != nil {
			// Counting failure to deliver this counter's own alert would keep
			// the alert firing forever whenever the sink is rate-limited.
			if ev == "AlertRelayForwardFailures" {
				s.m.selfAlertFailures.Add(1)
			} else {
				s.m.forwardFailures.Add(1)
			}
			slog.Error("forward failed", "event", ev, "err", err)
			return
		}
		s.m.forwarded.Add(1)
		slog.Info("alert forwarded", "event", ev, "priority", n.Priority)
	}()
	w.WriteHeader(http.StatusAccepted)
}

// watchdogActive asks whether any Alertmanager replica holds the active
// Watchdog. A replica that answers "no alerts" is a healthy replica that
// vmalert's notifier connection is simply not pinned to, so a single
// load-balanced query would flap between true and false with the connection
// reshuffle of every pod restart.
func (s *server) watchdogActive(ctx context.Context) (bool, error) {
	urls, err := s.replicaURLs(ctx)
	if err != nil {
		return false, err
	}
	return s.watchdogActiveAny(ctx, urls)
}

// watchdogActiveAny reports whether any of the given replicas holds the
// active Watchdog. Replicas are queried in parallel: the headless Service
// publishes not-ready addresses, so a blackholed replica stays in DNS and
// must not be allowed to eat the poll budget ahead of the replica that
// actually holds the Watchdog.
func (s *server) watchdogActiveAny(ctx context.Context, urls []string) (bool, error) {
	type result struct {
		active bool
		err    error
	}
	// Buffered to len(urls) so stragglers finish into the channel and exit
	// after an early return; the caller cancels ctx right after.
	results := make(chan result, len(urls))
	for _, u := range urls {
		go func(u string) {
			active, err := s.watchdogActiveAt(ctx, u)
			results <- result{active: active, err: err}
		}(u)
	}
	var errs []error
	for range urls {
		r := <-results
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		if r.active {
			return true, nil
		}
	}
	if len(errs) == len(urls) {
		return false, errors.Join(errs...)
	}
	// Partial failures with no Watchdog anywhere reachable count as "not
	// seen" — the replica holding it may be the one that is down — but are
	// still logged so a flapping replica is visible before the page fires.
	for _, e := range errs {
		slog.Warn("watchdog replica query failed", "err", e)
	}
	return false, nil
}

// replicaURLs expands the configured Alertmanager URL into one base URL per
// replica. The Service in front of Alertmanager is headless, so a host
// lookup returns every pod IP; if resolution fails (or the host is already
// an address), the URL is used as configured.
func (s *server) replicaURLs(ctx context.Context) ([]string, error) {
	base, err := url.Parse(s.cfg.alertmanagerURL)
	if err != nil {
		return nil, err
	}
	if s.lookupHost == nil {
		return []string{s.cfg.alertmanagerURL}, nil
	}
	addrs, err := s.lookupHost(ctx, base.Hostname())
	if err != nil || len(addrs) == 0 {
		return []string{s.cfg.alertmanagerURL}, nil
	}
	urls := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		u := *base
		if port := base.Port(); port != "" {
			u.Host = net.JoinHostPort(addr, port)
		} else if strings.Contains(addr, ":") {
			u.Host = "[" + addr + "]"
		} else {
			u.Host = addr
		}
		urls = append(urls, u.String())
	}
	return urls, nil
}

// watchdogActiveAt queries one Alertmanager replica for the always-firing
// Watchdog alert.
func (s *server) watchdogActiveAt(ctx context.Context, baseURL string) (bool, error) {
	u := baseURL + "/api/v2/alerts?filter=" + url.QueryEscape(`alertname="Watchdog"`)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false, fmt.Errorf("alertmanager responded %d", resp.StatusCode)
	}
	var alerts []struct {
		Status struct {
			State string `json:"state"`
		} `json:"status"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&alerts); err != nil {
		return false, err
	}
	for _, a := range alerts {
		if a.Status.State == "active" || a.Status.State == "" {
			return true, nil
		}
	}
	return false, nil
}

func (s *server) runDeadman(ctx context.Context, d *deadman) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		active, err := s.watchdogActive(qctx)
		cancel()
		if err != nil {
			slog.Warn("watchdog query failed", "err", err)
		}
		now := time.Now()
		action := d.observe(now, active)
		s.m.watchdogLastSeen.Store(d.lastSeen.Unix())
		if d.silent {
			s.m.pipelineSilent.Store(1)
		} else {
			s.m.pipelineSilent.Store(0)
		}
		var n notification
		switch action {
		case dmPage, dmRepage:
			n = notification{
				Title:    "alerting pipeline silent",
				Priority: 5,
				Tags:     []string{"critical", "deadman"},
				Body: fmt.Sprintf("alerting pipeline silent — vmalert/alertmanager/scrape path broken: no active Watchdog for %s (threshold %s)",
					now.Sub(d.lastSeen).Round(time.Second), d.timeout),
			}
		case dmRecover:
			n = notification{
				Title:    "alerting pipeline recovered",
				Priority: 3,
				Tags:     []string{"deadman"},
				Body:     "Watchdog alert active again — alerting pipeline restored",
			}
		default:
			continue
		}
		// Detached from the run context so shutdown never cuts off an
		// in-flight page; inflight lets main wait for it. A page is only
		// recorded once the sink accepted it — a failed page retries on the
		// next 60s tick instead of waiting out the repage window.
		s.inflight.Add(1)
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryBudget)
		if err := s.out.deliver(dctx, n); err != nil {
			slog.Error("dead-man page delivery failed", "err", err)
		} else {
			slog.Info("dead-man notification sent", "title", n.Title)
			if action == dmPage || action == dmRepage {
				d.pageDelivered(now)
			}
		}
		cancel()
		s.inflight.Done()
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	m := &metrics{}
	// Boot grace: the dead-man counts silence from process start, so a
	// restart never pages before WATCHDOG_TIMEOUT of real silence.
	start := time.Now()
	m.watchdogLastSeen.Store(start.Unix())

	srv := &server{
		cfg:        cfg,
		m:          m,
		// The pace floor mirrors ntfy.sh's free-tier replenish rate (one
		// visitor request per ~5s): a storm coalesces into digests instead
		// of racing the sink's limiter into an IP ban.
		out: newPacedSink(
			&ntfySink{url: cfg.ntfyURL, token: cfg.ntfyToken, client: &http.Client{Timeout: 10 * time.Second}, sleep: sleepCtx},
			5*time.Second,
			&m.coalesced,
		),
		loc:        pagerLocation(),
		client:     &http.Client{Timeout: 10 * time.Second},
		lookupHost: net.DefaultResolver.LookupHost,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /slack", srv.handleSlack)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.render(w)
	})

	httpSrv := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Handlers respond before delivery (202 + detached goroutine), so
		// this only covers parsing the payload and writing the response.
		WriteTimeout: 15 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	go srv.runDeadman(ctx, newDeadman(start, cfg.watchdogTimeout, time.Hour))

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		slog.Info("shutting down")
		cancel()
		// Grace covers a full in-flight delivery (deliveryBudget) so a page
		// mid-retry survives the restart.
		sctx, scancel := context.WithTimeout(context.Background(), deliveryBudget)
		defer scancel()
		_ = httpSrv.Shutdown(sctx)
	}()

	slog.Info("listening", "addr", cfg.listenAddr, "alertmanager", cfg.alertmanagerURL, "watchdog_timeout", cfg.watchdogTimeout.String())
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
	// Delivery goroutines are deadline-bounded (deliveryBudget), so this
	// wait terminates; without it an accepted alert could be abandoned at
	// exit.
	srv.inflight.Wait()
}
