package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/stripe/stripe-go/v83"
	"github.com/stripe/stripe-go/v83/webhook"
	tb "github.com/tigerbeetle/tigerbeetle-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/guardian-intelligence/guardian/src/services/payments/paymentdb"
)

const maximumWebhookBytes = 1 << 20

type paymentServer struct {
	cfg              config
	queries          *paymentdb.Queries
	stripe           stripeRail
	verifier         tokenVerifier
	metrics          *paymentMetrics
	databaseReady    func(context.Context) error
	tigerBeetleReady func() error
}

func (s *paymentServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", s.handleReady)
	mux.Handle("GET /metrics", promhttp.Handler())
	mux.HandleFunc("POST /api/payments/v1/checkout-sessions", s.handleCustomerCheckout)
	mux.HandleFunc("GET /api/payments/v1/orders/{id}", s.handleCustomerOrder)
	mux.HandleFunc("POST /api/payments/v1/stripe/webhook", s.handleStripeWebhook)
	mux.HandleFunc("GET /api/payments/v1/canary", s.handleCanaryPage)
	mux.HandleFunc(
		"POST /api/payments/v1/canary/checkout-session",
		s.handleCheckoutCanarySession,
	)
	mux.HandleFunc("POST /api/payments/v1/canary/checkout", s.handleCheckoutCanary)
	mux.HandleFunc(
		"POST /api/payments/v1/canary/runs/{id}/complete",
		s.handleCompleteCanary,
	)
	mux.HandleFunc("POST /internal/payments/canary/rail", s.handleRailCanary)
	mux.HandleFunc("GET /internal/payments/canary/runs/latest", s.handleLatestCanaries)
	return s.traceMiddleware(mux)
}

func (s *paymentServer) traceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(
			r.Context(),
			propagation.HeaderCarrier(r.Header),
		)
		ctx, span := otel.Tracer(paymentsServiceName).Start(
			ctx,
			r.Method+" "+routeName(r.URL.Path),
			trace.WithSpanKind(trace.SpanKindServer),
		)
		defer span.End()
		span.SetAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("url.path", routeName(r.URL.Path)),
		)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func routeName(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/payments/v1/orders/"):
		return "/api/payments/v1/orders/{id}"
	case strings.HasPrefix(path, "/api/payments/v1/canary/runs/"):
		return "/api/payments/v1/canary/runs/{id}/complete"
	case strings.HasPrefix(path, "/internal/payments/canary/runs/"):
		return "/internal/payments/canary/runs/{id}"
	default:
		return path
	}
}

func (s *paymentServer) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.databaseReady(ctx); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.tigerBeetleReady(); err != nil {
		http.Error(w, "ledger unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready\n"))
}

func (s *paymentServer) handleCanaryPage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set(
		"Content-Security-Policy",
		"default-src 'none'; connect-src 'self'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'",
	)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.WriteString(
		w,
		"<!doctype html><html><head><title>Guardian checkout canary</title></head><body><main id=\"canary-ready\">Guardian checkout canary</main></body></html>",
	)
}

type createCheckoutBody struct {
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

func (s *paymentServer) handleCustomerCheckout(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.CustomerCheckoutEnabled {
		http.Error(w, "customer checkout is not admitted", http.StatusServiceUnavailable)
		return
	}
	rawToken, err := bearerToken(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	identity, err := s.verifier.Verify(r.Context(), rawToken)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body createCheckoutBody
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	if body.Currency != "usd" || body.AmountCents < 50 || body.AmountCents > 99_999_999 {
		http.Error(w, "invalid amount or currency", http.StatusBadRequest)
		return
	}
	result, err := s.createCheckout(
		r.Context(),
		identity.TenantID,
		body.AmountCents,
		"customer",
	)
	if err != nil {
		http.Error(w, "checkout unavailable", http.StatusServiceUnavailable)
		return
	}
	writeCheckout(w, result, "")
}

func (s *paymentServer) handleCustomerOrder(w http.ResponseWriter, r *http.Request) {
	rawToken, err := bearerToken(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	identity, err := s.verifier.Verify(r.Context(), rawToken)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	order, err := s.queries.GetOrder(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && order.TenantID != identity.TenantID) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "order unavailable", http.StatusServiceUnavailable)
		return
	}
	writeOrder(w, order)
}

type checkoutResult struct {
	Order   paymentdb.PaymentOrder
	Session *stripe.CheckoutSession
}

func (s *paymentServer) createCheckout(
	ctx context.Context,
	tenantID string,
	amount int64,
	lane string,
) (checkoutResult, error) {
	ctx, span := otel.Tracer(paymentsServiceName).Start(ctx, "checkout.create_session")
	defer span.End()
	orderID := tb.ID().String()
	traceID := traceIDFromContext(ctx)
	if traceID == "" {
		return checkoutResult{}, errors.New("trace context unavailable")
	}
	order, err := s.queries.CreateOrder(ctx, paymentdb.CreateOrderParams{
		ID:                orderID,
		TenantID:          tenantID,
		ProviderAccountID: s.cfg.StripeAccountID,
		Currency:          "usd",
		AmountCents:       amount,
		TraceID:           traceID,
	})
	if err != nil {
		s.metrics.checkoutRequests.WithLabelValues("database_error").Inc()
		return checkoutResult{}, fmt.Errorf("create order: %w", err)
	}
	priceID := ""
	if lane == "checkout" {
		priceID = s.cfg.StripeCanaryPriceID
	}
	session, err := s.stripe.CreateCheckoutSession(ctx, checkoutRequest{
		OrderID:     order.ID,
		AmountCents: order.AmountCents,
		Currency:    order.Currency,
		TraceID:     order.TraceID,
		PriceID:     priceID,
		SuccessURL: checkoutReturnURL(
			s.cfg.PublicBaseURL,
			"/payments/complete",
			order.ID,
		),
		CancelURL: checkoutReturnURL(
			s.cfg.PublicBaseURL,
			"/payments/canceled",
			order.ID,
		),
	})
	if err != nil {
		_, _ = s.queries.MarkOrderFailed(ctx, paymentdb.MarkOrderFailedParams{
			ID:           order.ID,
			FailureClass: textValue("stripe_create_session"),
		})
		s.metrics.checkoutRequests.WithLabelValues("stripe_error").Inc()
		span.RecordError(err)
		span.SetStatus(codes.Error, "Stripe session creation")
		return checkoutResult{}, fmt.Errorf("create Stripe checkout session: %w", err)
	}
	order, err = s.queries.SetOrderCheckoutSession(
		ctx,
		paymentdb.SetOrderCheckoutSessionParams{
			ID:                      order.ID,
			StripeCheckoutSessionID: textValue(session.ID),
		},
	)
	if err != nil {
		s.metrics.checkoutRequests.WithLabelValues("database_error").Inc()
		return checkoutResult{}, fmt.Errorf("persist Stripe checkout session: %w", err)
	}
	s.metrics.checkoutRequests.WithLabelValues("success").Inc()
	span.SetAttributes(
		attribute.String("guardian.order_id", order.ID),
		attribute.String("guardian.lane", lane),
		attribute.String("stripe.checkout_session_id", session.ID),
	)
	return checkoutResult{Order: order, Session: session}, nil
}

func writeCheckout(w http.ResponseWriter, result checkoutResult, runID string) {
	response := map[string]any{
		"order_id":   result.Order.ID,
		"session_id": result.Session.ID,
		"url":        result.Session.URL,
		"trace_id":   result.Order.TraceID,
	}
	if runID != "" {
		response["run_id"] = runID
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *paymentServer) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maximumWebhookBytes))
	if err != nil {
		s.metrics.webhookReceipts.WithLabelValues("oversize").Inc()
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	event, err := webhook.ConstructEvent(
		body,
		r.Header.Get("Stripe-Signature"),
		s.cfg.StripeWebhookSecret,
	)
	if err != nil {
		s.metrics.webhookReceipts.WithLabelValues("invalid_signature").Inc()
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	if event.Livemode {
		s.metrics.webhookReceipts.WithLabelValues("live_mode_rejected").Inc()
		http.Error(w, "event mode rejected", http.StatusUnprocessableEntity)
		return
	}
	if event.Account != "" && event.Account != s.cfg.StripeAccountID {
		s.metrics.webhookReceipts.WithLabelValues("account_rejected").Inc()
		http.Error(w, "event account rejected", http.StatusUnprocessableEntity)
		return
	}
	_, err = s.queries.InsertProviderEvent(r.Context(), paymentdb.InsertProviderEventParams{
		ProviderAccountID: s.cfg.StripeAccountID,
		EventID:           event.ID,
		EventType:         string(event.Type),
		ObjectID:          textValue(stripeObjectID(event.Data.Raw)),
		ApiVersion:        textValue(event.APIVersion),
		Livemode:          event.Livemode,
		Payload:           body,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		s.metrics.webhookReceipts.WithLabelValues("duplicate").Inc()
		w.WriteHeader(http.StatusOK)
		return
	}
	if err != nil {
		s.metrics.webhookReceipts.WithLabelValues("durability_error").Inc()
		http.Error(w, "event not durable", http.StatusServiceUnavailable)
		return
	}
	s.metrics.webhookReceipts.WithLabelValues("accepted").Inc()
	w.WriteHeader(http.StatusOK)
}

func (s *paymentServer) handleCheckoutCanary(w http.ResponseWriter, r *http.Request) {
	s.handlePaymentCanary(w, r, "checkout")
}

func (s *paymentServer) handleRailCanary(w http.ResponseWriter, r *http.Request) {
	s.handlePaymentCanary(w, r, "rail")
}

func (s *paymentServer) handlePaymentCanary(
	w http.ResponseWriter,
	r *http.Request,
	lane string,
) {
	if !s.authorizeCanary(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.CanaryCompletionDeadline)
	defer cancel()
	spanName := "canary.stripe_to_tigerbeetle"
	if lane == "checkout" {
		spanName = "canary.browser_to_tigerbeetle"
	}
	ctx, span := otel.Tracer(paymentsServiceName).Start(ctx, spanName)
	defer span.End()
	traceID := traceIDFromContext(ctx)
	runID := tb.ID().String()
	run, err := s.queries.StartCanaryRun(ctx, paymentdb.StartCanaryRunParams{
		ID:      runID,
		TraceID: traceID,
		Lane:    lane,
	})
	if err != nil {
		http.Error(w, "canary unavailable", http.StatusServiceUnavailable)
		return
	}
	started := time.Now()
	orderID := tb.ID().String()
	order, err := s.queries.CreateOrder(ctx, paymentdb.CreateOrderParams{
		ID:                orderID,
		TenantID:          "synthetic-canary",
		ProviderAccountID: s.cfg.StripeAccountID,
		Currency:          "usd",
		AmountCents:       s.cfg.CanaryAmountCents,
		TraceID:           traceID,
	})
	if err == nil {
		err = s.queries.AttachCanaryOrder(ctx, paymentdb.AttachCanaryOrderParams{
			ID:      run.ID,
			OrderID: textValue(order.ID),
		})
	}
	if err == nil {
		var intent *stripe.PaymentIntent
		intent, err = s.stripe.CreateCanaryPayment(ctx, checkoutRequest{
			OrderID:     order.ID,
			AmountCents: order.AmountCents,
			Currency:    order.Currency,
			TraceID:     order.TraceID,
		})
		if err == nil {
			_, err = s.queries.SetOrderPaymentIntent(
				ctx,
				paymentdb.SetOrderPaymentIntentParams{
					ID:                    order.ID,
					StripePaymentIntentID: textValue(intent.ID),
				},
			)
		}
	}
	if err == nil {
		err = s.waitForLedger(ctx, order.ID)
	}
	status := "passed"
	failureClass := pgtype.Text{}
	httpStatus := http.StatusOK
	if err != nil {
		status = "failed"
		failureClass = textValue(errorClass(err))
		httpStatus = http.StatusServiceUnavailable
		span.RecordError(err)
		span.SetStatus(codes.Error, "canary failed")
	}
	_, completionErr := s.queries.CompleteCanaryRun(
		context.WithoutCancel(r.Context()),
		paymentdb.CompleteCanaryRunParams{
			ID:           run.ID,
			Status:       status,
			FailureClass: failureClass,
		},
	)
	if completionErr != nil && err == nil {
		err = completionErr
		httpStatus = http.StatusServiceUnavailable
		status = "failed"
	}
	s.metrics.canaryRuns.WithLabelValues(lane, status).Inc()
	if status == "passed" {
		s.metrics.canaryLastSuccess.WithLabelValues(lane).SetToCurrentTime()
		s.metrics.endToEndSeconds.WithLabelValues(lane).Observe(time.Since(started).Seconds())
	}
	writeJSON(w, httpStatus, map[string]any{
		"run_id":        run.ID,
		"order_id":      orderID,
		"status":        status,
		"failure_class": failureClass.String,
		"trace_id":      traceID,
	})
}

func (s *paymentServer) handleCheckoutCanarySession(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeCanary(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	traceID := traceIDFromContext(r.Context())
	run, err := s.queries.StartCanaryRun(r.Context(), paymentdb.StartCanaryRunParams{
		ID:      tb.ID().String(),
		TraceID: traceID,
		Lane:    "checkout",
	})
	if err != nil {
		http.Error(w, "canary unavailable", http.StatusServiceUnavailable)
		return
	}
	result, err := s.createCheckout(
		r.Context(),
		"synthetic-canary",
		s.cfg.CanaryAmountCents,
		"checkout",
	)
	if err == nil {
		err = s.queries.AttachCanaryOrder(
			r.Context(),
			paymentdb.AttachCanaryOrderParams{
				ID:      run.ID,
				OrderID: textValue(result.Order.ID),
			},
		)
	}
	if err != nil {
		_, _ = s.queries.CompleteCanaryRun(
			context.WithoutCancel(r.Context()),
			paymentdb.CompleteCanaryRunParams{
				ID:           run.ID,
				Status:       "failed",
				FailureClass: textValue("checkout_session_creation"),
			},
		)
		s.metrics.canaryRuns.WithLabelValues("checkout", "failed").Inc()
		http.Error(w, "canary unavailable", http.StatusServiceUnavailable)
		return
	}
	writeCheckout(w, result, run.ID)
}

func (s *paymentServer) handleCompleteCanary(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeCanary(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		Status        string `json:"status"`
		FailureClass  string `json:"failure_class"`
		TraceVerified bool   `json:"trace_verified"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		return
	}
	if body.Status != "passed" && body.Status != "failed" {
		http.Error(w, "invalid status", http.StatusBadRequest)
		return
	}
	run, err := s.queries.GetCanaryRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, pgx.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "canary run unavailable", http.StatusServiceUnavailable)
		return
	}
	if body.Status == "passed" {
		if !body.TraceVerified {
			http.Error(w, "trace proof is required", http.StatusBadRequest)
			return
		}
		if !run.OrderID.Valid {
			http.Error(w, "canary order unavailable", http.StatusConflict)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.CanaryCompletionDeadline)
		defer cancel()
		if err := s.waitForLedger(ctx, run.OrderID.String); err != nil {
			body.Status = "failed"
			body.FailureClass = "ledger_completion"
		}
	}
	run, err = s.queries.CompleteCanaryRun(r.Context(), paymentdb.CompleteCanaryRunParams{
		ID:           r.PathValue("id"),
		Status:       body.Status,
		FailureClass: textValue(body.FailureClass),
	})
	if err != nil {
		http.Error(w, "canary run unavailable", http.StatusServiceUnavailable)
		return
	}
	s.metrics.canaryRuns.WithLabelValues(run.Lane, body.Status).Inc()
	if body.Status == "passed" {
		s.metrics.canaryLastSuccess.WithLabelValues(run.Lane).SetToCurrentTime()
		if run.StartedAt.Valid {
			s.metrics.endToEndSeconds.WithLabelValues(run.Lane).Observe(
				time.Since(run.StartedAt.Time).Seconds(),
			)
		}
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *paymentServer) handleLatestCanaries(w http.ResponseWriter, r *http.Request) {
	if !s.authorizeCanary(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	runs, err := s.queries.LatestCanaryRuns(r.Context())
	if err != nil {
		http.Error(w, "canary status unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *paymentServer) waitForLedger(ctx context.Context, orderID string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		order, err := s.queries.GetOrder(ctx, orderID)
		if err != nil {
			return err
		}
		switch order.Status {
		case "ledger_posted":
			return nil
		case "failed":
			return fmt.Errorf("order failed: %s", order.FailureClass.String)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *paymentServer) authorizeCanary(r *http.Request) bool {
	token, err := bearerToken(r)
	if err != nil || len(token) != len(s.cfg.CanaryToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.CanaryToken)) == 1
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return errors.New("multiple JSON values")
	}
	return nil
}

func writeOrder(w http.ResponseWriter, order paymentdb.PaymentOrder) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           order.ID,
		"status":       order.Status,
		"synthetic":    order.Synthetic,
		"amount_cents": order.AmountCents,
		"currency":     order.Currency,
		"trace_id":     order.TraceID,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("encode response", "error", err)
	}
}
