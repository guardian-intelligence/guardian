package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"

	analyticsv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/analytics/v1"
	"github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/analytics/v1/analyticsv1connect"
)

type captureSink struct {
	mu   sync.Mutex
	rows []eventRow
}

func (c *captureSink) Insert(_ context.Context, rows []eventRow) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rows = append(c.rows, rows...)
	return nil
}

func (c *captureSink) snapshot() []eventRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]eventRow(nil), c.rows...)
}

// newTestStack returns a live httptest server backed by a capture sink with
// an immediate-flush batcher (maxRows 1).
func newTestStack(t *testing.T) (*httptest.Server, *captureSink) {
	t.Helper()
	sink := &captureSink{}
	b := newBatcher(sink, 1, time.Hour, 1000)
	t.Cleanup(b.Close)
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	svc := &eventService{batch: b, now: func() time.Time { return time.UnixMilli(1_800_000_000_000) }, validate: v}
	srv := httptest.NewServer(newHandler(svc))
	t.Cleanup(srv.Close)
	return srv, sink
}

func publishClient(srv *httptest.Server, opts ...connect.ClientOption) analyticsv1connect.EventServiceClient {
	return analyticsv1connect.NewEventServiceClient(srv.Client(), srv.URL+"/api/events", opts...)
}

func waitRows(t *testing.T, sink *captureSink, n int) []eventRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rows := sink.snapshot(); len(rows) >= n {
			return rows
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d rows (have %d)", n, len(sink.snapshot()))
	return nil
}

func TestPublishAcceptsAndDerives(t *testing.T) {
	srv, sink := newTestStack(t)
	client := publishClient(srv)

	req := connect.NewRequest(&analyticsv1.PublishRequest{
		SentAtUnixMs: 1_800_000_000_000 - 2500,
		Events: []*analyticsv1.Event{
			{Name: "company.route_view", Path: "/letters", SessionSeq: 7},
			{Name: "web_vital.lcp", Path: "/", VitalName: "LCP", VitalValue: 2100, SessionSeq: 8},
			{Name: "not.registered", Path: "/"},
		},
	})
	res, err := client.Publish(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.GetAccepted() != 2 || res.Msg.GetRejected() != 1 {
		t.Fatalf("accepted=%d rejected=%d, want 2/1", res.Msg.GetAccepted(), res.Msg.GetRejected())
	}
	rows := waitRows(t, sink, 2)
	r := rows[0]
	if r.ClientSkewMs != 2500 {
		t.Errorf("skew = %d, want 2500", r.ClientSkewMs)
	}
	if r.SessionSeq != 7 || r.SchemaVersion != 1 {
		t.Errorf("seq/version = %d/%d", r.SessionSeq, r.SchemaVersion)
	}
	// httptest host is 127.0.0.1:port — not a guardian host.
	if r.Site != "local" {
		t.Errorf("site = %q, want local", r.Site)
	}
	if cookieHeader(res.Header()) == "" {
		t.Error("expected a minted correlation cookie on first contact")
	}
}

func cookieHeader(h http.Header) string {
	for _, v := range h.Values("Set-Cookie") {
		if strings.HasPrefix(v, correlationCookie+"=") {
			return v
		}
	}
	return ""
}

// The trust boundary: without the ingress-stamped source header the row
// must be client_claimed with an unspecified IP no matter what the client
// sends; with it (only the controller can set it on the real path — it
// overwrites client copies), the row is edge_verified with that IP.
func TestTrustTierDerivation(t *testing.T) {
	srv, sink := newTestStack(t)

	forged := connect.NewRequest(&analyticsv1.PublishRequest{
		SentAtUnixMs: 1, Events: []*analyticsv1.Event{{Name: "page_view", Path: "/"}},
	})
	// A direct-to-origin attacker can set any headers EXCEPT survive the
	// controller's proxySetHeaders overwrite; simulate the forged-direct
	// case by setting the IP header without the source header.
	forged.Header().Set("X-Guardian-Client-Ip", "203.0.113.99")
	if _, err := publishClient(srv).Publish(context.Background(), forged); err != nil {
		t.Fatal(err)
	}
	rows := waitRows(t, sink, 1)
	if rows[0].TrustTier != tierClientClaimed {
		t.Fatalf("forged: tier = %d, want client_claimed", rows[0].TrustTier)
	}
	if rows[0].IPSource != "" || rows[0].ClientIP.String() != "::" {
		t.Fatalf("forged: ip=%s source=%q, want unspecified/empty", rows[0].ClientIP, rows[0].IPSource)
	}

	verified := connect.NewRequest(&analyticsv1.PublishRequest{
		SentAtUnixMs: 1, Events: []*analyticsv1.Event{{Name: "page_view", Path: "/"}},
	})
	verified.Header().Set("X-Guardian-Client-Ip", "2001:db8::7")
	verified.Header().Set("X-Guardian-Client-Ip-Source", "cloudflare")
	if _, err := publishClient(srv).Publish(context.Background(), verified); err != nil {
		t.Fatal(err)
	}
	rows = waitRows(t, sink, 2)
	last := rows[len(rows)-1]
	if last.TrustTier != tierEdgeVerified || last.IPSource != "cloudflare" || last.ClientIP.String() != "2001:db8::7" {
		t.Fatalf("verified: tier=%d ip=%s source=%q", last.TrustTier, last.ClientIP, last.IPSource)
	}
}

func TestPublishBatchCaps(t *testing.T) {
	srv, _ := newTestStack(t)
	client := publishClient(srv)

	over := make([]*analyticsv1.Event, maxBatchEvents+1)
	for i := range over {
		over[i] = &analyticsv1.Event{Name: "page_view", Path: "/"}
	}
	_, err := client.Publish(context.Background(), connect.NewRequest(&analyticsv1.PublishRequest{
		SentAtUnixMs: 1, Events: over,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized batch: err = %v, want invalid_argument", err)
	}

	_, err = client.Publish(context.Background(), connect.NewRequest(&analyticsv1.PublishRequest{SentAtUnixMs: 1}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty batch: err = %v, want invalid_argument", err)
	}
}

func TestCorrelationCookieRoundTrip(t *testing.T) {
	srv, sink := newTestStack(t)
	client := publishClient(srv)

	req := connect.NewRequest(&analyticsv1.PublishRequest{
		SentAtUnixMs: 1, Events: []*analyticsv1.Event{{Name: "page_view", Path: "/"}},
	})
	req.Header().Set("Cookie", correlationCookie+"=0f0e0d0c-0b0a-0908-0706-050403020100")
	if _, err := client.Publish(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	rows := waitRows(t, sink, 1)
	want := [16]byte{0x0f, 0x0e, 0x0d, 0x0c, 0x0b, 0x0a, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}
	if rows[0].CorrelationID != want {
		t.Fatalf("correlation id = %x", rows[0].CorrelationID)
	}
}

// JSON over plain POST is exactly what navigator.sendBeacon produces (Blob
// with application/json); the wire the beacon uses must keep working.
func TestPublishPlainJSONPost(t *testing.T) {
	srv, sink := newTestStack(t)
	body := `{"sentAtUnixMs":"1799999999000","events":[{"name":"page_view","path":"/","sessionSeq":1}]}`
	res, err := srv.Client().Post(
		srv.URL+"/api/events/guardian.analytics.v1.EventService/Publish",
		"application/json",
		strings.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	rows := waitRows(t, sink, 1)
	if rows[0].EventName != "page_view" || rows[0].ClientSkewMs != 1000 {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestBodyLimit(t *testing.T) {
	srv, _ := newTestStack(t)
	big := strings.Repeat("x", 257<<10)
	res, err := srv.Client().Post(
		srv.URL+"/api/events/guardian.analytics.v1.EventService/Publish",
		"application/json",
		strings.NewReader(`{"sentAtUnixMs":"1","events":[{"name":"page_view","propsJson":"`+big+`"}]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Fatal("oversized body must not return 200")
	}
}
