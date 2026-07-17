package main

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guardian-intelligence/guardian/src/services/payments/paymentdb"
)

type paymentMetrics struct {
	checkoutRequests      *prometheus.CounterVec
	webhookReceipts       *prometheus.CounterVec
	providerEventsPending prometheus.Gauge
	providerEventAge      prometheus.Gauge
	unmatchedTransactions prometheus.Gauge
	journalIncomplete     prometheus.Gauge
	canaryRuns            *prometheus.CounterVec
	canaryLastSuccess     *prometheus.GaugeVec
	endToEndSeconds       *prometheus.HistogramVec
	accountBinding        prometheus.Gauge
	reconciliationSuccess prometheus.Gauge
	reconciliationErrors  prometheus.Counter
}

func newPaymentMetrics(registerer prometheus.Registerer) *paymentMetrics {
	metrics := &paymentMetrics{
		checkoutRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "guardian_payments_checkout_requests_total",
			Help: "Checkout-session creation attempts by outcome.",
		}, []string{"outcome"}),
		webhookReceipts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "guardian_payments_webhook_receipts_total",
			Help: "Stripe webhook receipts by verification outcome.",
		}, []string{"outcome"}),
		providerEventsPending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_payments_provider_events_pending",
			Help: "Durable Stripe events not yet projected.",
		}),
		providerEventAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_payments_oldest_provider_event_age_seconds",
			Help: "Age of the oldest unprocessed Stripe event.",
		}),
		unmatchedTransactions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_payments_unmatched_balance_transactions",
			Help: "Stripe balance transactions unmatched for at least fifteen minutes.",
		}),
		journalIncomplete: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_payments_journal_incomplete_commands",
			Help: "TigerBeetle commands missing an intent, accepted result, or outcome journal record.",
		}),
		canaryRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "guardian_payments_canary_runs_total",
			Help: "Payment canary runs by lane and outcome.",
		}, []string{"lane", "outcome"}),
		canaryLastSuccess: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "guardian_payments_canary_last_success_timestamp_seconds",
			Help: "Unix timestamp of the last successful payment canary.",
		}, []string{"lane"}),
		endToEndSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "guardian_payments_end_to_end_seconds",
			Help:    "Stripe creation to durable TigerBeetle outcome latency.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 20, 40, 60},
		}, []string{"lane"}),
		accountBinding: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_payments_stripe_account_binding",
			Help: "One when the restricted key resolves to the configured Stripe sandbox account.",
		}),
		reconciliationSuccess: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "guardian_payments_reconciliation_last_success_timestamp_seconds",
			Help: "Unix timestamp of the latest successful Stripe balance reconciliation.",
		}),
		reconciliationErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "guardian_payments_reconciliation_errors_total",
			Help: "Stripe balance reconciliation failures.",
		}),
	}
	registerer.MustRegister(
		metrics.checkoutRequests,
		metrics.webhookReceipts,
		metrics.providerEventsPending,
		metrics.providerEventAge,
		metrics.unmatchedTransactions,
		metrics.journalIncomplete,
		metrics.canaryRuns,
		metrics.canaryLastSuccess,
		metrics.endToEndSeconds,
		metrics.accountBinding,
		metrics.reconciliationSuccess,
		metrics.reconciliationErrors,
	)
	return metrics
}

func (m *paymentMetrics) refresh(ctx context.Context, queries *paymentdb.Queries) {
	pending, err := queries.CountPendingProviderEvents(ctx)
	if err == nil {
		m.providerEventsPending.Set(float64(pending))
	}
	age, err := queries.OldestPendingProviderEventAgeSeconds(ctx)
	if err == nil {
		m.providerEventAge.Set(float64(age))
	}
	unmatched, err := queries.CountUnmatchedBalanceTransactions(ctx)
	if err == nil {
		m.unmatchedTransactions.Set(float64(unmatched))
	}
	incomplete, err := queries.CountJournalIncomplete(ctx)
	if err == nil {
		m.journalIncomplete.Set(float64(incomplete))
	}
}

func (m *paymentMetrics) refreshLoop(
	ctx context.Context,
	queries *paymentdb.Queries,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		m.refresh(ctx, queries)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
