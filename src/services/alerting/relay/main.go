// alert-relay — the pluggable delivery seam of the alerting pipeline:
// Slack-incoming-webhook JSON in (the de-facto payload format both Alerta's
// slack plugin and Flagger AlertProviders emit), ntfy out. The sink sits
// behind an interface and is chosen by config, so swapping ntfy for another
// pager is a config change, not a payload-format migration.
//
// It also runs the pipeline's dead-man's switch: vmalert evaluates an
// always-firing Watchdog rule, the relay polls Alertmanager for it, and a
// missing Watchdog becomes a page — silence anywhere in the
// scrape→vmalert→alertmanager path pages instead of staying quiet.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"log/slog"
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
var alertaSummaryRe = regexp.MustCompile(`^\*\[(?P<status>[^\]]+)\] +(?P<environment>\S+) +(?P<service>.*?) +(?P<severity>\S+)\* - _(?P<event>.+) on (?P<resource>.+?)_(?: <[^>]*>)?$`)

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
		status:      group("status"),
		environment: group("environment"),
		service:     group("service"),
		severity:    group("severity"),
		event:       group("event"),
		resource:    group("resource"),
	}, true
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

type metrics struct {
	forwarded        atomic.Uint64
	forwardFailures  atomic.Uint64
	suppressed       atomic.Uint64
	watchdogLastSeen atomic.Int64
	pipelineSilent   atomic.Int64
}

func (m *metrics) render(w io.Writer) {
	fmt.Fprintf(w, "# HELP relay_forwarded_total Alerts forwarded to the sink.\n")
	fmt.Fprintf(w, "# TYPE relay_forwarded_total counter\n")
	fmt.Fprintf(w, "relay_forwarded_total %d\n", m.forwarded.Load())
	fmt.Fprintf(w, "# HELP relay_forward_failures_total Alerts that failed delivery after all retries.\n")
	fmt.Fprintf(w, "# TYPE relay_forward_failures_total counter\n")
	fmt.Fprintf(w, "relay_forward_failures_total %d\n", m.forwardFailures.Load())
	fmt.Fprintf(w, "# HELP relay_heartbeats_suppressed_total Watchdog/Heartbeat payloads counted but not forwarded.\n")
	fmt.Fprintf(w, "# TYPE relay_heartbeats_suppressed_total counter\n")
	fmt.Fprintf(w, "relay_heartbeats_suppressed_total %d\n", m.suppressed.Load())
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

func (s *ntfySink) deliver(ctx context.Context, n notification) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			s.sleep(ctx, time.Duration(attempt*2)*time.Second)
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
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ntfy responded %d", resp.StatusCode)
	}
	return nil
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
	client *http.Client
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
		if err := s.out.deliver(dctx, n); err != nil {
			s.m.forwardFailures.Add(1)
			slog.Error("forward failed", "event", ev, "err", err)
			return
		}
		s.m.forwarded.Add(1)
		slog.Info("alert forwarded", "event", ev, "priority", n.Priority)
	}()
	w.WriteHeader(http.StatusAccepted)
}

// watchdogActive queries Alertmanager for the always-firing Watchdog alert.
func (s *server) watchdogActive(ctx context.Context) (bool, error) {
	u := s.cfg.alertmanagerURL + "/api/v2/alerts?filter=" + url.QueryEscape(`alertname="Watchdog"`)
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
		cfg:    cfg,
		m:      m,
		out:    &ntfySink{url: cfg.ntfyURL, token: cfg.ntfyToken, client: &http.Client{Timeout: 10 * time.Second}, sleep: sleepCtx},
		client: &http.Client{Timeout: 10 * time.Second},
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
