package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The events writer feeds the cockpit timeline: it polls the repo's main
// branch through the GitHub commits API and persists one row per commit —
// squash merges carry the PR title and number in the subject, promotion
// bot commits are how Kargo ships, so the default branch's history IS the
// build/ship event stream. Conditional requests keep the poll nearly free:
// a 304 does not count against the unauthenticated rate limit, so steady
// state costs one counted request per burst of new commits.

func runEvents(args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	repo := fs.String("repo", "", "GitHub owner/repo to poll (required)")
	branch := fs.String("branch", "main", "branch whose history feeds the timeline")
	listen := fs.String("listen", ":8080", "healthz/metrics listen address")
	poll := fs.Duration("poll", time.Minute, "poll cadence")
	retention := fs.Duration("retention", 24*time.Hour, "row retention horizon")
	_ = fs.Parse(args)
	if *repo == "" {
		return errors.New("--repo is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Same PG* env convention and pool bound as the rollup writer.
	pool, err := pgxpool.New(ctx, "pool_max_conns=2")
	if err != nil {
		return fmt.Errorf("pg config: %w", err)
	}
	defer pool.Close()

	m := &eventsMetrics{}
	w := &eventsWriter{
		pool:      pool,
		m:         m,
		retention: *retention,
		url:       fmt.Sprintf("https://api.github.com/repos/%s/commits?sha=%s&per_page=50", *repo, *branch),
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	go w.runPoller(ctx, *poll)
	go w.runPruner(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /metrics", func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.render(rw)
	})
	slog.Info("events listening", "addr", *listen, "repo", *repo, "branch", *branch)
	return serveUntilSignal(ctx, cancel, *listen, mux)
}

// eventRow is one timeline entry, mirroring the cockpit_events table.
type eventRow struct {
	id    string
	ts    time.Time
	kind  string
	actor string
	title string
	url   string
}

// ghCommit is the slice of the commits API response the timeline needs.
type ghCommit struct {
	Sha    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	HTMLURL string `json:"html_url"`
}

var prSubjectRe = regexp.MustCompile(`\(#\d+\)$`)

// commitRows maps commits to timeline rows. Kinds: a squash-merge subject
// ends in "(#N)" and is a landed PR; a [bot] author is a promotion (Kargo
// is the only bot committing to main); anything else is a plain push.
func commitRows(commits []ghCommit) []eventRow {
	rows := make([]eventRow, 0, len(commits))
	for _, c := range commits {
		if c.Sha == "" {
			continue
		}
		actor := c.Commit.Author.Name
		if c.Author != nil && c.Author.Login != "" {
			actor = c.Author.Login
		}
		subject, _, _ := strings.Cut(c.Commit.Message, "\n")
		kind := "push"
		switch {
		case prSubjectRe.MatchString(subject):
			kind = "pr_merged"
		case strings.HasSuffix(actor, "[bot]"):
			kind = "promotion"
		}
		rows = append(rows, eventRow{
			id:    c.Sha,
			ts:    c.Commit.Committer.Date,
			kind:  kind,
			actor: actor,
			title: subject,
			url:   c.HTMLURL,
		})
	}
	return rows
}

type eventsWriter struct {
	pool      *pgxpool.Pool
	m         *eventsMetrics
	retention time.Duration
	url       string
	client    *http.Client
	etag      string
}

const insertEventSQL = `INSERT INTO cockpit_events
	(id, ts, kind, actor, title, url)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (id) DO NOTHING`

func (w *eventsWriter) writeRows(ctx context.Context, rows []eventRow) error {
	if len(rows) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, r := range rows {
		b.Queue(insertEventSQL, r.id, r.ts, r.kind, r.actor, r.title, r.url)
	}
	if err := w.pool.SendBatch(ctx, b).Close(); err != nil {
		return err
	}
	w.m.rowsWritten.Add(uint64(len(rows)))
	return nil
}

// runPoller keeps the timeline current. Failure logging is edge-triggered,
// the sampler-client discipline: one warn on entering a failing state, one
// info on recovery.
func (w *eventsWriter) runPoller(ctx context.Context, cadence time.Duration) {
	degraded := false
	for ctx.Err() == nil {
		if err := w.pollOnce(ctx); err != nil {
			w.m.pollFailures.Add(1)
			if !degraded && ctx.Err() == nil {
				slog.Warn("events poll failing; retrying each cadence", "err", err)
				degraded = true
			}
		} else {
			if degraded {
				slog.Info("events poll recovered")
				degraded = false
			}
			w.m.lastPollTsMs.Store(time.Now().UnixMilli())
		}
		sleepCtx(ctx, cadence)
	}
}

func (w *eventsWriter) pollOnce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if w.etag != "" {
		req.Header.Set("If-None-Match", w.etag)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
		// fall through to decode
	case http.StatusForbidden, http.StatusTooManyRequests:
		// Rate limited despite the conditional-request budget; wait out the
		// window instead of burning the remaining quota on failures.
		if reset, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
			if wait := time.Until(time.Unix(reset, 0)); wait > 0 {
				slog.Warn("events poll rate limited; backing off", "until", time.Unix(reset, 0).UTC().Format(time.RFC3339))
				sleepCtx(ctx, wait)
			}
		}
		return fmt.Errorf("rate limited: %s", resp.Status)
	default:
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	var commits []ghCommit
	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	// Only remember the ETag once its payload is durably written: a failed
	// write with a remembered ETag would 304 forever and drop the burst.
	if err := w.writeRows(ctx, commitRows(commits)); err != nil {
		return err
	}
	w.etag = resp.Header.Get("ETag")
	return nil
}

func (w *eventsWriter) runPruner(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tag, err := w.pool.Exec(ctx,
				`DELETE FROM cockpit_events WHERE ts < now() - make_interval(secs => $1)`,
				w.retention.Seconds())
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("events prune failed", "err", err)
				}
				continue
			}
			w.m.rowsPruned.Add(uint64(tag.RowsAffected()))
		}
	}
}
