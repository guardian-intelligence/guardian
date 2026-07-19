package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stripe/stripe-go/v83"
	"github.com/stripe/stripe-go/v83/webhook"
	tb "github.com/tigerbeetle/tigerbeetle-go"

	"github.com/guardian-intelligence/guardian/src/services/payments/paymentdb"
	"github.com/guardian-intelligence/guardian/src/services/postflight/controlplane/pgtest"
)

type fakeStripe struct {
	intent *stripe.PaymentIntent
}

func (f *fakeStripe) VerifyAccount(_ context.Context, expected string) error {
	if expected != "acct_sandbox" {
		return errors.New("account mismatch")
	}
	return nil
}

func (f *fakeStripe) CreateCheckoutSession(
	_ context.Context,
	request checkoutRequest,
) (*stripe.CheckoutSession, error) {
	return &stripe.CheckoutSession{
		ID:       "cs_test_" + request.OrderID,
		Livemode: false,
		URL:      "https://checkout.stripe.test/session",
	}, nil
}

func (f *fakeStripe) CreateCanaryPayment(
	_ context.Context,
	_ checkoutRequest,
) (*stripe.PaymentIntent, error) {
	return f.intent, nil
}

func (f *fakeStripe) RetrievePaymentIntent(
	_ context.Context,
	_ string,
) (*stripe.PaymentIntent, error) {
	return f.intent, nil
}

func (f *fakeStripe) ListRecentBalanceTransactions(
	_ context.Context,
	_ int64,
) ([]*stripe.BalanceTransaction, error) {
	return nil, nil
}

type fakeTigerBeetle struct {
	mu        sync.Mutex
	accounts  map[string]tb.Account
	transfers map[string]tb.Transfer
}

func newFakeTigerBeetle() *fakeTigerBeetle {
	return &fakeTigerBeetle{
		accounts:  make(map[string]tb.Account),
		transfers: make(map[string]tb.Transfer),
	}
}

func (f *fakeTigerBeetle) CreateAccounts(accounts []tb.Account) ([]tb.CreateAccountResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	results := make([]tb.CreateAccountResult, len(accounts))
	for index, account := range accounts {
		status := tb.AccountCreated
		if existing, ok := f.accounts[account.ID.String()]; ok {
			if existing != account {
				return nil, errors.New("account changed on retry")
			}
			status = tb.AccountExists
		}
		f.accounts[account.ID.String()] = account
		results[index] = tb.CreateAccountResult{Status: status, Timestamp: uint64(index + 1)}
	}
	return results, nil
}

func (f *fakeTigerBeetle) CreateTransfers(
	transfers []tb.Transfer,
) ([]tb.CreateTransferResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	results := make([]tb.CreateTransferResult, len(transfers))
	for index, transfer := range transfers {
		status := tb.TransferCreated
		if existing, ok := f.transfers[transfer.ID.String()]; ok {
			if existing != transfer {
				return nil, errors.New("transfer changed on retry")
			}
			status = tb.TransferExists
		}
		f.transfers[transfer.ID.String()] = transfer
		results[index] = tb.CreateTransferResult{Status: status, Timestamp: uint64(index + 1)}
	}
	return results, nil
}

func (f *fakeTigerBeetle) LookupAccounts(ids []tb.Uint128) ([]tb.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tb.Account
	for _, id := range ids {
		if value, ok := f.accounts[id.String()]; ok {
			out = append(out, value)
		}
	}
	return out, nil
}

func (f *fakeTigerBeetle) LookupTransfers(ids []tb.Uint128) ([]tb.Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tb.Transfer
	for _, id := range ids {
		if value, ok := f.transfers[id.String()]; ok {
			out = append(out, value)
		}
	}
	return out, nil
}

func (f *fakeTigerBeetle) Nop() error { return nil }
func (f *fakeTigerBeetle) Close()     {}

func testDatabase(t *testing.T) (*pgxpool.Pool, *paymentdb.Queries) {
	t.Helper()
	if os.Getenv("PGTEST_INITDB") == "" {
		t.Skip("PGTEST_INITDB is supplied by the Bazel test target")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgtest.Start(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, conn.Conn()); err != nil {
		t.Fatal(err)
	}
	conn.Release()
	return pool, paymentdb.New(pool)
}

func successfulIntent(order paymentdb.PaymentOrder) *stripe.PaymentIntent {
	return &stripe.PaymentIntent{
		ID:             "pi_test_" + order.ID,
		Livemode:       false,
		Status:         stripe.PaymentIntentStatusSucceeded,
		AmountReceived: order.AmountCents,
		Currency:       stripe.CurrencyUSD,
		Metadata: map[string]string{
			"guardian_order_id": order.ID,
			"guardian_lane":     "synthetic",
			"guardian_trace_id": order.TraceID,
		},
		LatestCharge: &stripe.Charge{
			ID: "ch_test_" + order.ID,
			BalanceTransaction: &stripe.BalanceTransaction{
				ID:                "txn_test_" + order.ID,
				Amount:            order.AmountCents,
				Fee:               2,
				Net:               order.AmountCents - 2,
				Currency:          stripe.CurrencyUSD,
				Created:           time.Now().Unix(),
				AvailableOn:       time.Now().Add(24 * time.Hour).Unix(),
				ReportingCategory: "charge",
				Type:              "charge",
				Source: &stripe.BalanceTransactionSource{
					ID:   "ch_test_" + order.ID,
					Type: "charge",
				},
			},
		},
	}
}

func createTestOrder(t *testing.T, queries *paymentdb.Queries) paymentdb.PaymentOrder {
	t.Helper()
	order, err := queries.CreateOrder(context.Background(), paymentdb.CreateOrderParams{
		ID:                tb.ID().String(),
		OrganizationID:    "synthetic-canary",
		ProviderAccountID: "acct_sandbox",
		Currency:          "usd",
		AmountCents:       50,
		TraceID:           "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatal(err)
	}
	return order
}

func TestProjectPaymentIsIdempotentAndBalanced(t *testing.T) {
	_, queries := testDatabase(t)
	order := createTestOrder(t, queries)
	intent := successfulIntent(order)
	rail := &fakeStripe{intent: intent}
	client := newFakeTigerBeetle()
	journal := newMemoryJournal()
	gateway := &ledgerGateway{queries: queries, tb: client, journal: journal}
	projector := &providerProjector{
		accountID: "acct_sandbox",
		queries:   queries,
		stripe:    rail,
		ledger:    gateway,
	}
	rawIntent, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	event := stripe.Event{
		ID:       "evt_test_payment",
		Type:     "payment_intent.succeeded",
		Livemode: false,
		Data:     &stripe.EventData{Raw: rawIntent},
	}
	if err := projector.projectSucceededPayment(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	firstTransferIDs := make(map[string]struct{}, len(client.transfers))
	for id := range client.transfers {
		firstTransferIDs[id] = struct{}{}
	}
	if len(firstTransferIDs) != 3 {
		t.Fatalf("expected capture, fee, and grant transfers; got %d", len(firstTransferIDs))
	}
	if err := projector.projectSucceededPayment(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(client.transfers) != 3 {
		t.Fatalf("duplicate event created transfers: %d", len(client.transfers))
	}
	posted, err := queries.GetOrder(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != "ledger_posted" {
		t.Fatalf("order status = %s", posted.Status)
	}
	if len(journal.intents) != len(journal.outcomes) || len(journal.intents) != 5 {
		t.Fatalf("journal intents=%d outcomes=%d", len(journal.intents), len(journal.outcomes))
	}
}

func TestProjectorRejectsLiveModeAndAmountMismatch(t *testing.T) {
	_, queries := testDatabase(t)
	order := createTestOrder(t, queries)
	intent := successfulIntent(order)
	rail := &fakeStripe{intent: intent}
	client := newFakeTigerBeetle()
	projector := &providerProjector{
		accountID: "acct_sandbox",
		queries:   queries,
		stripe:    rail,
		ledger: &ledgerGateway{
			queries: queries,
			tb:      client,
			journal: newMemoryJournal(),
		},
	}
	liveRaw, _ := json.Marshal(stripe.Event{
		ID:       "evt_live",
		Type:     "payment_intent.succeeded",
		Livemode: true,
	})
	err := projector.projectEvent(context.Background(), paymentdb.ProviderEvent{
		ProviderAccountID: "acct_sandbox",
		Livemode:          true,
		Payload:           liveRaw,
	})
	if err == nil {
		t.Fatal("live-mode event was accepted")
	}
	intent.AmountReceived++
	rawIntent, _ := json.Marshal(intent)
	err = projector.projectSucceededPayment(context.Background(), stripe.Event{
		ID:   "evt_bad_amount",
		Type: "payment_intent.succeeded",
		Data: &stripe.EventData{Raw: rawIntent},
	})
	if err == nil {
		t.Fatal("amount mismatch was accepted")
	}
	if len(client.transfers) != 0 {
		t.Fatal("negative test mutated TigerBeetle")
	}
}

func TestWebhookSignatureAndDeduplication(t *testing.T) {
	pool, queries := testDatabase(t)
	registry := prometheus.NewRegistry()
	server := &paymentServer{
		cfg: config{
			StripeAccountID:     "acct_sandbox",
			StripeWebhookSecret: "whsec_test",
		},
		queries: queries,
		metrics: newPaymentMetrics(registry),
		databaseReady: func(ctx context.Context) error {
			return pool.Ping(ctx)
		},
		tigerBeetleReady: func() error { return nil },
	}
	event := stripe.Event{
		ID:         "evt_signed",
		Object:     "event",
		Type:       "payment_intent.created",
		Livemode:   false,
		APIVersion: stripe.APIVersion,
		Data:       &stripe.EventData{Raw: json.RawMessage(`{"id":"pi_test"}`)},
	}
	payload, _ := json.Marshal(event)

	bad := httptest.NewRequest(http.MethodPost, "/api/payments/v1/stripe/webhook", bytes.NewReader(payload))
	bad.Header.Set("Stripe-Signature", "invalid")
	badResponse := httptest.NewRecorder()
	server.handleStripeWebhook(badResponse, bad)
	if badResponse.Code != http.StatusBadRequest {
		t.Fatalf("bad signature status = %d", badResponse.Code)
	}
	if pending, err := queries.CountPendingProviderEvents(context.Background()); err != nil || pending != 0 {
		t.Fatalf("bad signature became durable: pending=%d err=%v", pending, err)
	}

	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: payload,
		Secret:  "whsec_test",
	})
	if _, err := webhook.ConstructEvent(
		signed.Payload,
		signed.Header,
		"whsec_test",
	); err != nil {
		t.Fatalf("construct generated signed payload: %v", err)
	}
	for index := 0; index < 2; index++ {
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/payments/v1/stripe/webhook",
			bytes.NewReader(signed.Payload),
		)
		request.Header.Set("Stripe-Signature", signed.Header)
		response := httptest.NewRecorder()
		server.handleStripeWebhook(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf(
				"signed webhook %d status = %d body=%q",
				index,
				response.Code,
				response.Body.String(),
			)
		}
	}
	if pending, err := queries.CountPendingProviderEvents(context.Background()); err != nil || pending != 1 {
		t.Fatalf("duplicate webhook pending=%d err=%v", pending, err)
	}
}

func TestMemoryJournalRejectsChangedRetry(t *testing.T) {
	journal := newMemoryJournal()
	if err := journal.WriteIntent(context.Background(), "command", map[string]int{"value": 1}); err != nil {
		t.Fatal(err)
	}
	if err := journal.WriteIntent(context.Background(), "command", map[string]int{"value": 2}); err == nil {
		t.Fatal("changed journal retry was accepted")
	}
}

func TestBrowserCanaryRequiresScopedToken(t *testing.T) {
	server := &paymentServer{cfg: config{CanaryToken: "canary-secret"}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/payments/v1/canary/checkout",
		nil,
	)
	response := httptest.NewRecorder()
	server.handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("browser canary without scoped token status = %d", response.Code)
	}
}

func TestConfigRejectsLiveStripeKeys(t *testing.T) {
	keys := []string{
		"DATABASE_URL",
		"PUBLIC_BASE_URL",
		"OIDC_ISSUER",
		"OIDC_CLIENT_ID",
		"AUTHORIZATION_API_URL",
		"AUTHORIZATION_CHECK_TOKEN",
		"STRIPE_API_KEY",
		"STRIPE_ACCOUNT_ID",
		"STRIPE_WEBHOOK_SECRET",
		"STRIPE_CANARY_PRICE_ID",
		"TIGERBEETLE_ADDRESSES",
		"TIGERBEETLE_CLUSTER_ID",
		"JOURNAL_S3_ENDPOINT",
		"JOURNAL_S3_BUCKET",
		"JOURNAL_S3_ACCESS_KEY_ID",
		"JOURNAL_S3_SECRET_ACCESS_KEY",
		"JOURNAL_S3_PREFIX",
		"PAYMENTS_CANARY_TOKEN",
	}
	for _, key := range keys {
		t.Setenv(key, "x")
	}
	t.Setenv("DATABASE_URL", "postgres://example")
	t.Setenv("PUBLIC_BASE_URL", "https://guardianintelligence.org")
	t.Setenv("OIDC_ISSUER", "https://guardianintelligence.org/realms/guardianintelligence.org")
	t.Setenv("OIDC_CLIENT_ID", "postflight-web")
	t.Setenv("AUTHORIZATION_API_URL", "http://authorization-api")
	t.Setenv("STRIPE_API_KEY", "sk_live_not_allowed")
	t.Setenv("STRIPE_ACCOUNT_ID", "acct_sandbox")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_test")
	t.Setenv("STRIPE_CANARY_PRICE_ID", "price_canary")
	t.Setenv("TIGERBEETLE_ADDRESSES", "10.8.0.11:3000,10.8.0.12:3000,10.8.0.13:3000")
	t.Setenv("TIGERBEETLE_CLUSTER_ID", acceptedTigerBeetleClusterID)
	if _, err := loadConfig(); err == nil {
		t.Fatal("live Stripe key was accepted")
	}

	t.Setenv("STRIPE_API_KEY", "rk_test_sandbox")
	t.Setenv("CUSTOMER_CHECKOUT_ENABLED", "true")
	if _, err := loadConfig(); err == nil {
		t.Fatal("customer checkout was admitted before ledger 1 hardening")
	}
}

func TestParseTigerBeetleClusterID(t *testing.T) {
	clusterID, err := parseTigerBeetleClusterID(acceptedTigerBeetleClusterID)
	if err != nil {
		t.Fatalf("parse accepted cluster ID: %v", err)
	}
	if got := clusterID.BigInt().String(); got != acceptedTigerBeetleClusterID {
		t.Fatalf("cluster ID = %s, want %s", got, acceptedTigerBeetleClusterID)
	}

	for _, value := range []string{
		"",
		"0",
		"1",
		"-1",
		"not-a-number",
		"340282366920938463463374607431768211456",
	} {
		if _, err := parseTigerBeetleClusterID(value); err == nil {
			t.Errorf("cluster ID %q was accepted", value)
		}
	}
}
