package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	analyticsv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/analytics/v1"
	"github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/analytics/v1/analyticsv1connect"
)

const schemaVersion = 1

type eventService struct {
	batch *batcher
	now   func() time.Time
}

// Connect handlers see only the RPC message; the trust-bearing material
// (verified-IP headers, Host, cookies, UA) lives on the http.Request. This
// middleware captures the derived request context plus the raw request so
// Publish can read cookies and set the minted-cookie response header.
type requestCtxKey struct{}

type requestMeta struct {
	ctx    requestContext
	corrID [16]byte
	minted *http.Cookie
}

func withRequestMeta(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, minted := correlationID(r)
		meta := &requestMeta{ctx: deriveRequestContext(r), corrID: id, minted: minted}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestCtxKey{}, meta)))
	})
}

func (s *eventService) Publish(
	ctx context.Context,
	req *connect.Request[analyticsv1.PublishRequest],
) (*connect.Response[analyticsv1.PublishResponse], error) {
	meta, _ := ctx.Value(requestCtxKey{}).(*requestMeta)
	if meta == nil {
		return nil, connect.NewError(connect.CodeInternal, nil)
	}
	events := req.Msg.GetEvents()
	if len(events) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errEmptyBatch)
	}
	if len(events) > maxBatchEvents {
		// Oversized batches reject wholesale, never truncate.
		return nil, connect.NewError(connect.CodeInvalidArgument, errBatchTooLarge)
	}

	now := s.now()
	skew := clampSkewMs(now.UnixMilli(), req.Msg.GetSentAtUnixMs())

	rows := make([]eventRow, 0, len(events))
	rejects := map[rejectReason]int{}
	for _, e := range events {
		if reason := validateEvent(e); reason != "" {
			rejects[reason]++
			continue
		}
		row := eventRow{
			ServerTs:      now,
			Site:          meta.ctx.Site,
			EventName:     e.GetName(),
			TrustTier:     meta.ctx.TrustTier,
			SchemaVersion: schemaVersion,
			CorrelationID: meta.corrID,
			SessionSeq:    e.GetSessionSeq(),
			Path:          e.GetPath(),
			Referrer:      truncate(e.GetReferrer(), maxReferrerLen),
			UA:            meta.ctx.UA,
			ClientIP:      meta.ctx.ClientIP,
			IPSource:      meta.ctx.IPSource,
			ClientSkewMs:  skew,
			VitalName:     e.GetVitalName(),
			VitalValue:    e.GetVitalValue(),
			Props:         e.GetPropsJson(),
		}
		copy(row.TraceID[:], e.GetTraceId())
		rows = append(rows, row)
	}
	if len(rows) > 0 {
		s.batch.Add(rows)
	}

	rejected := 0
	for reason, n := range rejects {
		rejected += n
		slog.Warn("events rejected", "reason", string(reason), "count", n,
			"site", meta.ctx.Site, "tier", meta.ctx.TrustTier)
	}

	res := connect.NewResponse(&analyticsv1.PublishResponse{
		Accepted: uint32(len(rows)),
		Rejected: uint32(rejected),
	})
	if meta.minted != nil {
		res.Header().Add("Set-Cookie", meta.minted.String())
	}
	return res, nil
}

var (
	errEmptyBatch    = constError("empty batch")
	errBatchTooLarge = constError("batch exceeds event cap")
)

type constError string

func (e constError) Error() string { return string(e) }

// newHandler mounts the Connect service under /api/events so the public
// route is /api/events/guardian.analytics.v1.EventService/Publish —
// path-prefix routed to this service by the ingress, same apex-sharing
// pattern as IAM prod.
func newHandler(svc *eventService) http.Handler {
	mux := http.NewServeMux()
	path, handler := analyticsv1connect.NewEventServiceHandler(svc)
	mux.Handle("/api/events"+path, http.StripPrefix("/api/events", handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return withRequestMeta(withBodyLimit(mux, 256<<10))
}

func withBodyLimit(next http.Handler, limit int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
