package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Turn is one message of the extracted transcript.
type Turn struct {
	Role    string
	Content string
}

// Conversation is what an adapter extracts from a live share page.
type Conversation struct {
	Provider string
	Model    string
	Turns    []Turn
}

// ShareSource adapts one provider's share-link format. Match is pure URL
// shape; Fetch hits the live page and extracts the transcript.
type ShareSource interface {
	Match(u *url.URL) bool
	Fetch(ctx context.Context, u *url.URL) (*Conversation, error)
}

// parserVersion is bumped whenever extraction logic changes, so
// parse_failed rows record which parser gave up and can be retried after a
// fix.
const parserVersion = 1

// ChatGPT-only at launch (2026-06-11 scope cut). claudeSource stays
// compiled and unit-tested; re-add here when Claude support ships.
var sources = []ShareSource{chatgptSource{}}

// Ingest sentinels, mapped to result pages by the handler.
var (
	ErrNotShareLink = errors.New("not a recognized share link")
	ErrGone         = errors.New("share link does not answer")
	ErrParse        = errors.New("could not extract the conversation")
)

// httpClient is shared by adapters; tests swap the Transport to serve
// fixtures. Redirects are not followed: a public share page answers 200
// directly, and a redirect almost always means a login wall.
var httpClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// canonicalShareURL validates and normalizes a submitted link: https, a
// known host, a share path. The canonical string is the dedup key.
func canonicalShareURL(raw string) (*url.URL, ShareSource, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil, ErrNotShareLink
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, ErrNotShareLink
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, nil, ErrNotShareLink
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Hostname()) // drops any port
	u.RawQuery, u.Fragment, u.User = "", "", nil
	u.Path = strings.TrimSuffix(u.Path, "/")
	for _, s := range sources {
		if s.Match(u) {
			return u, s, nil
		}
	}
	return nil, nil, ErrNotShareLink
}

// fetchPage GETs a share page and returns the body when it answers 200 with
// HTML. Anything else — 4xx, 5xx, a redirect to a login wall — is ErrGone:
// the link is not publicly alive, which is the entire anti-abuse bar.
// source names the adapter for the dependency-plane metrics; the failure
// reasons are counted here, at the branches, because the ErrGone wrapping
// erases which one fired.
func fetchPage(ctx context.Context, source string, u *url.URL) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		fetchTotal.WithLabelValues(source, "network").Inc()
		return "", fmt.Errorf("%w: %v", ErrGone, err)
	}
	// A browser-ish UA: share pages are public but some edges serve bot
	// UAs a challenge page instead of the document.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:140.0) Gecko/20100101 Firefox/140.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	// The duration brackets the upstream exchange — request through body
	// read, success or failure (a 20s timeout is exactly what the fetch-p95
	// alert watches for) — and excludes parsing, which happens in the caller.
	start := time.Now()
	defer func() {
		fetchDuration.WithLabelValues(source).Observe(time.Since(start).Seconds())
	}()
	resp, err := httpClient.Do(req)
	if err != nil {
		fetchTotal.WithLabelValues(source, "network").Inc()
		return "", fmt.Errorf("%w: %v", ErrGone, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fetchTotal.WithLabelValues(source, "http_status").Inc()
		return "", fmt.Errorf("%w: status %d", ErrGone, resp.StatusCode)
	}
	// Share pages run ~100KB-2MB; 8MB is a hard stop on hostile bodies.
	const limit = 8 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		fetchTotal.WithLabelValues(source, "network").Inc()
		return "", fmt.Errorf("%w: %v", ErrGone, err)
	}
	if len(body) > limit {
		fetchTotal.WithLabelValues(source, "body_too_large").Inc()
		return "", fmt.Errorf("%w: body exceeds %d bytes", ErrGone, limit)
	}
	return string(body), nil
}

// extractJSON returns the balanced JSON value ([...] or {...}) starting at
// s[start], honoring strings and escapes. Adapters use it to cut embedded
// state objects out of HTML without parsing the whole page.
func extractJSON(s string, start int) (string, bool) {
	if start >= len(s) {
		return "", false
	}
	open := s[start]
	var close byte
	switch open {
	case '{':
		close = '}'
	case '[':
		close = ']'
	default:
		return "", false
	}
	depth := 0
	inStr := false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch c {
			case '\\':
				i++
			case '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
