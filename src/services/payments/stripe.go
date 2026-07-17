package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/stripe/stripe-go/v83"
)

type checkoutRequest struct {
	OrderID     string
	AmountCents int64
	Currency    string
	TraceID     string
	SuccessURL  string
	CancelURL   string
	PriceID     string
}

type stripeRail interface {
	VerifyAccount(context.Context, string) error
	CreateCheckoutSession(context.Context, checkoutRequest) (*stripe.CheckoutSession, error)
	CreateCanaryPayment(context.Context, checkoutRequest) (*stripe.PaymentIntent, error)
	RetrievePaymentIntent(context.Context, string) (*stripe.PaymentIntent, error)
	ListRecentBalanceTransactions(context.Context, int64) ([]*stripe.BalanceTransaction, error)
}

type stripeClient struct {
	client *stripe.Client
}

func newStripeClient(apiKey, apiBase string) *stripeClient {
	if apiBase == "" {
		return &stripeClient{client: stripe.NewClient(apiKey)}
	}
	backends := stripe.NewBackendsWithConfig(&stripe.BackendConfig{
		URL:               stripe.String(apiBase),
		MaxNetworkRetries: stripe.Int64(2),
	})
	return &stripeClient{client: stripe.NewClient(apiKey, stripe.WithBackends(backends))}
}

func (s *stripeClient) VerifyAccount(ctx context.Context, expected string) error {
	account, err := s.client.V1Accounts.Retrieve(ctx, nil)
	if err != nil {
		return fmt.Errorf("retrieve Stripe account: %w", err)
	}
	if account.ID != expected {
		return fmt.Errorf("Stripe account mismatch: credential resolved to %s", account.ID)
	}
	return nil
}

func monitorStripeAccountBinding(
	ctx context.Context,
	rail stripeRail,
	expected string,
	metrics *paymentMetrics,
	interval time.Duration,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := rail.VerifyAccount(ctx, expected); err != nil {
				metrics.accountBinding.Set(0)
				slog.Error("Stripe sandbox account binding", "error", err)
				continue
			}
			metrics.accountBinding.Set(1)
		}
	}
}

func (s *stripeClient) CreateCheckoutSession(
	ctx context.Context,
	request checkoutRequest,
) (*stripe.CheckoutSession, error) {
	lineItem := &stripe.CheckoutSessionCreateLineItemParams{
		Quantity: stripe.Int64(1),
	}
	if request.PriceID != "" {
		lineItem.Price = stripe.String(request.PriceID)
	} else {
		lineItem.PriceData = &stripe.CheckoutSessionCreateLineItemPriceDataParams{
			Currency:   stripe.String(request.Currency),
			UnitAmount: stripe.Int64(request.AmountCents),
			ProductData: &stripe.CheckoutSessionCreateLineItemPriceDataProductDataParams{
				Name: stripe.String("Guardian purchased credit"),
			},
		}
	}
	params := &stripe.CheckoutSessionCreateParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModePayment)),
		SuccessURL:        stripe.String(request.SuccessURL),
		CancelURL:         stripe.String(request.CancelURL),
		LineItems:         []*stripe.CheckoutSessionCreateLineItemParams{lineItem},
		ClientReferenceID: stripe.String(request.OrderID),
		PaymentIntentData: &stripe.CheckoutSessionCreatePaymentIntentDataParams{
			Metadata: map[string]string{
				"guardian_order_id": request.OrderID,
				"guardian_lane":     "synthetic",
				"guardian_trace_id": request.TraceID,
			},
		},
		Metadata: map[string]string{
			"guardian_order_id": request.OrderID,
			"guardian_lane":     "synthetic",
			"guardian_trace_id": request.TraceID,
		},
	}
	params.SetIdempotencyKey("guardian-checkout-" + request.OrderID)
	return s.client.V1CheckoutSessions.Create(ctx, params)
}

func (s *stripeClient) CreateCanaryPayment(
	ctx context.Context,
	request checkoutRequest,
) (*stripe.PaymentIntent, error) {
	params := &stripe.PaymentIntentCreateParams{
		Amount:             stripe.Int64(request.AmountCents),
		Currency:           stripe.String(request.Currency),
		Confirm:            stripe.Bool(true),
		PaymentMethod:      stripe.String("pm_card_visa"),
		PaymentMethodTypes: []*string{stripe.String("card")},
		Metadata: map[string]string{
			"guardian_order_id": request.OrderID,
			"guardian_lane":     "synthetic",
			"guardian_trace_id": request.TraceID,
		},
	}
	params.SetIdempotencyKey("guardian-rail-canary-" + request.OrderID)
	return s.client.V1PaymentIntents.Create(ctx, params)
}

func (s *stripeClient) RetrievePaymentIntent(
	ctx context.Context,
	id string,
) (*stripe.PaymentIntent, error) {
	params := &stripe.PaymentIntentRetrieveParams{}
	params.AddExpand("latest_charge.balance_transaction")
	return s.client.V1PaymentIntents.Retrieve(ctx, id, params)
}

func (s *stripeClient) ListRecentBalanceTransactions(
	ctx context.Context,
	createdGTE int64,
) ([]*stripe.BalanceTransaction, error) {
	params := &stripe.BalanceTransactionListParams{}
	params.CreatedRange = &stripe.RangeQueryParams{GreaterThanOrEqual: createdGTE}
	params.Limit = stripe.Int64(100)
	var out []*stripe.BalanceTransaction
	for item, err := range s.client.V1BalanceTransactions.List(ctx, params) {
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func validatePaymentIntent(pi *stripe.PaymentIntent, orderID, traceID string, amount int64) error {
	if pi == nil {
		return errors.New("payment intent is nil")
	}
	if pi.Livemode {
		return errors.New("live-mode payment intent rejected")
	}
	if pi.Status != stripe.PaymentIntentStatusSucceeded {
		return fmt.Errorf("payment intent status is %s", pi.Status)
	}
	if pi.Currency != stripe.CurrencyUSD || pi.AmountReceived != amount {
		return errors.New("payment intent amount or currency mismatch")
	}
	if pi.Metadata["guardian_order_id"] != orderID ||
		pi.Metadata["guardian_lane"] != "synthetic" ||
		pi.Metadata["guardian_trace_id"] != traceID {
		return errors.New("payment intent metadata mismatch")
	}
	if pi.LatestCharge == nil || pi.LatestCharge.BalanceTransaction == nil {
		return errors.New("payment intent lacks expanded charge balance transaction")
	}
	bt := pi.LatestCharge.BalanceTransaction
	if bt.Amount != amount || bt.Amount-bt.Fee != bt.Net {
		return errors.New("Stripe gross, fee, and net invariant failed")
	}
	return nil
}

func stripeObjectID(raw json.RawMessage) string {
	var object struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &object)
	return object.ID
}

func checkoutReturnURL(base *url.URL, path, orderID string) string {
	u := *base
	u.Path = path
	query := u.Query()
	query.Set("order_id", orderID)
	u.RawQuery = query.Encode()
	return u.String()
}

func balanceTransactionTime(epoch int64) time.Time {
	return time.Unix(epoch, 0).UTC()
}
