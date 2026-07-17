package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stripe/stripe-go/v83"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/guardian-intelligence/guardian/src/services/payments/paymentdb"
)

type providerProjector struct {
	accountID string
	queries   *paymentdb.Queries
	stripe    stripeRail
	ledger    *ledgerGateway
}

func (p *providerProjector) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := p.processOne(ctx); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			slog.Error("provider event projection", "error_class", errorClass(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *providerProjector) processOne(ctx context.Context) error {
	event, err := p.queries.ClaimProviderEvent(ctx)
	if err != nil {
		return err
	}
	err = p.projectEvent(ctx, event)
	if err != nil {
		_ = p.queries.MarkProviderEventRetryable(
			ctx,
			paymentdb.MarkProviderEventRetryableParams{
				ProviderAccountID: event.ProviderAccountID,
				EventID:           event.EventID,
				LastErrorClass:    textValue(errorClass(err)),
			},
		)
		return err
	}
	return p.queries.MarkProviderEventProcessed(ctx, paymentdb.MarkProviderEventProcessedParams{
		ProviderAccountID: event.ProviderAccountID,
		EventID:           event.EventID,
	})
}

func (p *providerProjector) projectEvent(
	ctx context.Context,
	stored paymentdb.ProviderEvent,
) error {
	var event stripe.Event
	if err := json.Unmarshal(stored.Payload, &event); err != nil {
		return fmt.Errorf("decode provider event: %w", err)
	}
	if stored.Livemode || event.Livemode {
		return errors.New("live_mode_event")
	}
	if stored.ProviderAccountID != p.accountID {
		return errors.New("provider_account_mismatch")
	}
	switch event.Type {
	case "payment_intent.succeeded":
		return p.projectSucceededPayment(ctx, event)
	case "checkout.session.completed",
		"checkout.session.async_payment_succeeded",
		"payment_intent.created",
		"charge.succeeded",
		"charge.updated",
		"balance.available":
		return nil
	default:
		return nil
	}
}

func (p *providerProjector) projectSucceededPayment(
	ctx context.Context,
	event stripe.Event,
) error {
	var notification stripe.PaymentIntent
	if err := json.Unmarshal(event.Data.Raw, &notification); err != nil {
		return fmt.Errorf("decode payment intent event: %w", err)
	}
	orderID := notification.Metadata["guardian_order_id"]
	if orderID == "" {
		return nil
	}
	order, err := p.queries.GetOrder(ctx, orderID)
	if err != nil {
		return fmt.Errorf("get order: %w", err)
	}
	traceCtx := contextForPersistedTrace(ctx, order.TraceID)
	traceCtx, span := otel.Tracer(paymentsServiceName).Start(
		traceCtx,
		"stripe.payment_intent.succeeded",
	)
	defer span.End()
	span.SetAttributes(
		attribute.String("guardian.order_id", order.ID),
		attribute.String("stripe.event_id", event.ID),
		attribute.String("stripe.payment_intent_id", notification.ID),
	)
	intent, err := p.stripe.RetrievePaymentIntent(traceCtx, notification.ID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "retrieve payment intent")
		return fmt.Errorf("retrieve payment intent: %w", err)
	}
	if err := validatePaymentIntent(intent, order.ID, order.TraceID, order.AmountCents); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "payment intent validation")
		return err
	}
	charge := intent.LatestCharge
	balanceTransaction := charge.BalanceTransaction
	order, err = p.queries.MarkOrderProviderPaid(
		traceCtx,
		paymentdb.MarkOrderProviderPaidParams{
			ID:                         order.ID,
			StripePaymentIntentID:      textValue(intent.ID),
			StripeChargeID:             textValue(charge.ID),
			StripeBalanceTransactionID: textValue(balanceTransaction.ID),
		},
	)
	if err != nil {
		return fmt.Errorf("mark order provider paid: %w", err)
	}
	rawBalanceTransaction, err := json.Marshal(balanceTransaction)
	if err != nil {
		return err
	}
	_, err = p.queries.UpsertBalanceTransaction(
		traceCtx,
		paymentdb.UpsertBalanceTransactionParams{
			ProviderAccountID:    p.accountID,
			BalanceTransactionID: balanceTransaction.ID,
			SourceID:             nullableText(balanceTransaction.Source),
			ReportingCategory:    string(balanceTransaction.ReportingCategory),
			TransactionType:      string(balanceTransaction.Type),
			Currency:             string(balanceTransaction.Currency),
			GrossCents:           balanceTransaction.Amount,
			FeeCents:             balanceTransaction.Fee,
			NetCents:             balanceTransaction.Net,
			AvailableOn: timeValue(
				balanceTransactionTime(balanceTransaction.AvailableOn),
			),
			ProviderCreatedAt: timeValue(
				balanceTransactionTime(balanceTransaction.Created),
			),
			Raw:     rawBalanceTransaction,
			OrderID: textValue(order.ID),
		},
	)
	if err != nil {
		return fmt.Errorf("persist balance transaction: %w", err)
	}
	if err := p.ledger.ProjectPayment(traceCtx, ledgerProjection{
		Order: order,
		BalanceTransaction: struct {
			ID    string
			Gross int64
			Fee   int64
			Net   int64
		}{
			ID:    balanceTransaction.ID,
			Gross: balanceTransaction.Amount,
			Fee:   balanceTransaction.Fee,
			Net:   balanceTransaction.Net,
		},
	}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ledger projection")
		return err
	}
	projectedAt := timeValue(time.Now().UTC())
	_, err = p.queries.UpsertBalanceTransaction(
		traceCtx,
		paymentdb.UpsertBalanceTransactionParams{
			ProviderAccountID:    p.accountID,
			BalanceTransactionID: balanceTransaction.ID,
			SourceID:             nullableText(balanceTransaction.Source),
			ReportingCategory:    string(balanceTransaction.ReportingCategory),
			TransactionType:      string(balanceTransaction.Type),
			Currency:             string(balanceTransaction.Currency),
			GrossCents:           balanceTransaction.Amount,
			FeeCents:             balanceTransaction.Fee,
			NetCents:             balanceTransaction.Net,
			AvailableOn: timeValue(
				balanceTransactionTime(balanceTransaction.AvailableOn),
			),
			ProviderCreatedAt: timeValue(
				balanceTransactionTime(balanceTransaction.Created),
			),
			Raw:               rawBalanceTransaction,
			OrderID:           textValue(order.ID),
			LedgerProjectedAt: projectedAt,
		},
	)
	return err
}

type balanceReconciler struct {
	accountID string
	queries   *paymentdb.Queries
	stripe    stripeRail
	metrics   *paymentMetrics
}

func (r *balanceReconciler) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := r.reconcile(ctx); err != nil {
			r.metrics.reconciliationErrors.Inc()
			slog.Error("Stripe balance reconciliation", "error_class", errorClass(err))
		} else {
			r.metrics.reconciliationSuccess.SetToCurrentTime()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *balanceReconciler) reconcile(ctx context.Context) error {
	items, err := r.stripe.ListRecentBalanceTransactions(ctx, time.Now().Add(-15*time.Minute).Unix())
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.Amount-item.Fee != item.Net {
			return fmt.Errorf("balance transaction %s violates gross-fee=net", item.ID)
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		orderID := pgtype.Text{}
		projectedAt := pgtype.Timestamptz{}
		order, lookupErr := r.queries.GetOrderByBalanceTransaction(ctx, textValue(item.ID))
		if lookupErr == nil {
			orderID = textValue(order.ID)
			if order.LedgerPostedAt.Valid {
				projectedAt = order.LedgerPostedAt
			}
		} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return lookupErr
		}
		if _, err := r.queries.UpsertBalanceTransaction(
			ctx,
			paymentdb.UpsertBalanceTransactionParams{
				ProviderAccountID:    r.accountID,
				BalanceTransactionID: item.ID,
				SourceID:             nullableText(item.Source),
				ReportingCategory:    string(item.ReportingCategory),
				TransactionType:      string(item.Type),
				Currency:             string(item.Currency),
				GrossCents:           item.Amount,
				FeeCents:             item.Fee,
				NetCents:             item.Net,
				AvailableOn:          timeValue(balanceTransactionTime(item.AvailableOn)),
				ProviderCreatedAt:    timeValue(balanceTransactionTime(item.Created)),
				Raw:                  raw,
				OrderID:              orderID,
				LedgerProjectedAt:    projectedAt,
			},
		); err != nil {
			return err
		}
	}
	return nil
}

func textValue(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func timeValue(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value, Valid: !value.IsZero()}
}

func nullableText(source *stripe.BalanceTransactionSource) pgtype.Text {
	if source == nil {
		return pgtype.Text{}
	}
	return textValue(source.ID)
}

func errorClass(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, pgx.ErrNoRows):
		return "not_found"
	default:
		return "dependency_or_invariant"
	}
}
