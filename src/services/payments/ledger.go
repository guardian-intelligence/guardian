package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	tb "github.com/tigerbeetle/tigerbeetle-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"github.com/guardian-intelligence/guardian/src/services/payments/paymentdb"
)

const (
	syntheticLedger uint32 = 2
	atomsPerCent    uint64 = 10_000_000_000

	accountProcessorSettlement      uint16 = 1002
	accountProcessorPaymentClearing uint16 = 1003
	accountProcessorFeeExpense      uint16 = 1004
	accountCustomerCredit           uint16 = 2001

	transferProviderPaymentCapture uint16 = 1002
	transferProviderFeeAssess      uint16 = 1003
	transferGrantIssuePurchase     uint16 = 2003
)

type tigerBeetleClient interface {
	CreateAccounts([]tb.Account) ([]tb.CreateAccountResult, error)
	CreateTransfers([]tb.Transfer) ([]tb.CreateTransferResult, error)
	LookupAccounts([]tb.Uint128) ([]tb.Account, error)
	LookupTransfers([]tb.Uint128) ([]tb.Transfer, error)
	Nop() error
	Close()
}

type ledgerGateway struct {
	queries *paymentdb.Queries
	tb      tigerBeetleClient
	journal recoveryJournal
}

type ledgerProjection struct {
	Order              paymentdb.PaymentOrder
	BalanceTransaction struct {
		ID    string
		Gross int64
		Fee   int64
		Net   int64
	}
}

type journalAccount struct {
	ID          string `json:"id"`
	RegistryKey string `json:"registry_key"`
	Ledger      uint32 `json:"ledger"`
	Code        uint16 `json:"code"`
	Flags       uint16 `json:"flags"`
	UserData128 string `json:"user_data_128"`
	UserData64  uint64 `json:"user_data_64"`
	UserData32  uint32 `json:"user_data_32"`
}

type journalTransfer struct {
	ID              string `json:"id"`
	DebitAccountID  string `json:"debit_account_id"`
	CreditAccountID string `json:"credit_account_id"`
	Amount          string `json:"amount"`
	UserData128     string `json:"user_data_128"`
	UserData64      uint64 `json:"user_data_64"`
	UserData32      uint32 `json:"user_data_32"`
	Ledger          uint32 `json:"ledger"`
	Code            uint16 `json:"code"`
	Flags           uint16 `json:"flags"`
}

type journalAcceptance struct {
	Schema string   `json:"schema"`
	IDs    []string `json:"ids"`
	Status string   `json:"status"`
}

func (g *ledgerGateway) ProjectPayment(ctx context.Context, projection ledgerProjection) error {
	ctx, span := otel.Tracer(paymentsServiceName).Start(ctx, "tigerbeetle.project_payment")
	defer span.End()
	span.SetAttributes(
		attribute.String("guardian.order_id", projection.Order.ID),
		attribute.String("stripe.balance_transaction_id", projection.BalanceTransaction.ID),
		attribute.Int64("payment.gross_cents", projection.BalanceTransaction.Gross),
		attribute.Int64("payment.fee_cents", projection.BalanceTransaction.Fee),
		attribute.Int("tigerbeetle.ledger", int(syntheticLedger)),
	)
	if !projection.Order.Synthetic || projection.Order.ProviderAccountID == "" {
		return errors.New("only account-bound synthetic orders may enter ledger 2")
	}
	if projection.BalanceTransaction.Gross != projection.Order.AmountCents ||
		projection.BalanceTransaction.Gross-projection.BalanceTransaction.Fee !=
			projection.BalanceTransaction.Net {
		return errors.New("provider amount invariant failed")
	}

	settlement, err := g.ensureAccount(
		ctx,
		"stripe:"+projection.Order.ProviderAccountID+":usd:settlement",
		accountProcessorSettlement,
		tb.AccountFlags{History: true},
		1,
		1,
	)
	if err != nil {
		return err
	}
	clearing, err := g.ensureAccount(
		ctx,
		"stripe:"+projection.Order.ProviderAccountID+":usd:payment-clearing",
		accountProcessorPaymentClearing,
		tb.AccountFlags{History: true, DebitsMustNotExceedCredits: true},
		1,
		1,
	)
	if err != nil {
		return err
	}
	feeExpense, err := g.ensureAccount(
		ctx,
		"stripe:"+projection.Order.ProviderAccountID+":usd:fee-expense",
		accountProcessorFeeExpense,
		tb.AccountFlags{History: true},
		1,
		1,
	)
	if err != nil {
		return err
	}
	customerCredit, err := g.ensureAccount(
		ctx,
		"purchase:"+projection.Order.ID+":customer-credit",
		accountCustomerCredit,
		tb.AccountFlags{History: true, DebitsMustNotExceedCredits: true},
		1,
		0,
	)
	if err != nil {
		return err
	}

	command, err := g.queries.EnsureLedgerCommand(ctx, paymentdb.EnsureLedgerCommandParams{
		CommandKey:        "stripe-bt:" + projection.BalanceTransaction.ID,
		OrderID:           projection.Order.ID,
		CorrelationID:     tb.ID().String(),
		TransferCaptureID: tb.ID().String(),
		TransferFeeID: pgtype.Text{
			String: tb.ID().String(),
			Valid:  projection.BalanceTransaction.Fee > 0,
		},
		TransferGrantID: tb.ID().String(),
	})
	if err != nil {
		return fmt.Errorf("persist ledger command IDs: %w", err)
	}
	correlationID, err := tb.HexStringToUint128(command.CorrelationID)
	if err != nil {
		return fmt.Errorf("parse correlation ID: %w", err)
	}
	captureID, err := tb.HexStringToUint128(command.TransferCaptureID)
	if err != nil {
		return fmt.Errorf("parse capture transfer ID: %w", err)
	}
	grantID, err := tb.HexStringToUint128(command.TransferGrantID)
	if err != nil {
		return fmt.Errorf("parse grant transfer ID: %w", err)
	}
	gross := tb.ToUint128(uint64(projection.BalanceTransaction.Gross) * atomsPerCent)
	transfers := []tb.Transfer{
		{
			ID:              captureID,
			DebitAccountID:  settlement.ID,
			CreditAccountID: clearing.ID,
			Amount:          gross,
			UserData128:     correlationID,
			UserData64:      1,
			UserData32:      1,
			Ledger:          syntheticLedger,
			Code:            transferProviderPaymentCapture,
		},
	}
	if projection.BalanceTransaction.Fee > 0 {
		feeID, err := tb.HexStringToUint128(command.TransferFeeID.String)
		if err != nil {
			return fmt.Errorf("parse fee transfer ID: %w", err)
		}
		transfers = append(transfers, tb.Transfer{
			ID:              feeID,
			DebitAccountID:  feeExpense.ID,
			CreditAccountID: settlement.ID,
			Amount: tb.ToUint128(
				uint64(projection.BalanceTransaction.Fee) * atomsPerCent,
			),
			UserData128: correlationID,
			UserData64:  1,
			UserData32:  1,
			Ledger:      syntheticLedger,
			Code:        transferProviderFeeAssess,
		})
	}
	transfers = append(transfers, tb.Transfer{
		ID:              grantID,
		DebitAccountID:  clearing.ID,
		CreditAccountID: customerCredit.ID,
		Amount:          gross,
		UserData128:     correlationID,
		UserData64:      1,
		UserData32:      0,
		Ledger:          syntheticLedger,
		Code:            transferGrantIssuePurchase,
	})
	for index := range transfers[:len(transfers)-1] {
		transfers[index].Flags = tb.TransferFlags{Linked: true}.ToUint16()
	}
	envelope := map[string]any{
		"schema":                 "guardian.tigerbeetle.command.v1",
		"ledger":                 syntheticLedger,
		"provider":               "stripe",
		"provider_account_id":    projection.Order.ProviderAccountID,
		"balance_transaction_id": projection.BalanceTransaction.ID,
		"order_id":               projection.Order.ID,
		"transfers":              journalTransfers(transfers),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal ledger payload: %w", err)
	}
	if err := g.queries.SetLedgerCommandPayload(ctx, paymentdb.SetLedgerCommandPayloadParams{
		CommandKey: command.CommandKey,
		Payload:    payload,
	}); err != nil {
		return fmt.Errorf("persist ledger command payload: %w", err)
	}
	if err := g.journal.WriteIntent(ctx, command.CommandKey, envelope); err != nil {
		return fmt.Errorf("journal ledger intent: %w", err)
	}
	if err := g.queries.MarkLedgerCommandIntentJournaled(ctx, command.CommandKey); err != nil {
		return err
	}
	if command.OutcomeJournaledAt.Valid {
		_, err := g.queries.MarkOrderLedgerPosted(ctx, projection.Order.ID)
		return err
	}
	results, err := g.tb.CreateTransfers(transfers)
	if err != nil {
		return fmt.Errorf("submit TigerBeetle transfers: %w", err)
	}
	if len(results) != len(transfers) {
		return fmt.Errorf("TigerBeetle returned %d results for %d transfers", len(results), len(transfers))
	}
	for index, result := range results {
		if result.Status != tb.TransferCreated && result.Status != tb.TransferExists {
			return fmt.Errorf("TigerBeetle transfer %d: %s", index, result.Status)
		}
	}
	transferIDs := make([]string, 0, len(transfers))
	for _, transfer := range transfers {
		transferIDs = append(transferIDs, transfer.ID.String())
	}
	outcome := journalAcceptance{
		Schema: "guardian.tigerbeetle.acceptance.v1",
		IDs:    transferIDs,
		Status: "accepted",
	}
	outcomeJSON, err := json.Marshal(outcome)
	if err != nil {
		return err
	}
	if err := g.queries.MarkLedgerCommandAccepted(ctx, paymentdb.MarkLedgerCommandAcceptedParams{
		CommandKey: command.CommandKey,
		Result:     outcomeJSON,
	}); err != nil {
		return err
	}
	if err := g.journal.WriteOutcome(ctx, command.CommandKey, outcome); err != nil {
		return fmt.Errorf("journal ledger outcome: %w", err)
	}
	if err := g.queries.MarkLedgerCommandOutcomeJournaled(ctx, command.CommandKey); err != nil {
		return err
	}
	if _, err := g.queries.MarkOrderLedgerPosted(ctx, projection.Order.ID); err != nil {
		return err
	}
	return nil
}

type acceptedAccount struct {
	ID tb.Uint128
}

func (g *ledgerGateway) ensureAccount(
	ctx context.Context,
	registryKey string,
	code uint16,
	flags tb.AccountFlags,
	userData64 uint64,
	userData32 uint32,
) (acceptedAccount, error) {
	generatedID := tb.ID()
	generatedUserData := tb.ID()
	row, err := g.queries.EnsureLedgerAccount(ctx, paymentdb.EnsureLedgerAccountParams{
		RegistryKey: registryKey,
		AccountID:   generatedID.String(),
		Code:        int32(code),
		Flags:       int32(flags.ToUint16()),
		UserData128: generatedUserData.String(),
		UserData64:  int64(userData64),
		UserData32:  int32(userData32),
	})
	if err != nil {
		return acceptedAccount{}, fmt.Errorf("persist account %s: %w", registryKey, err)
	}
	id, err := tb.HexStringToUint128(row.AccountID)
	if err != nil {
		return acceptedAccount{}, err
	}
	userData, err := tb.HexStringToUint128(row.UserData128)
	if err != nil {
		return acceptedAccount{}, err
	}
	if row.AcceptedAt.Valid {
		return acceptedAccount{ID: id}, nil
	}
	account := tb.Account{
		ID:          id,
		UserData128: userData,
		UserData64:  uint64(row.UserData64),
		UserData32:  uint32(row.UserData32),
		Ledger:      uint32(row.Ledger),
		Code:        uint16(row.Code),
		Flags:       uint16(row.Flags),
	}
	journalID := "account-" + row.AccountID
	envelope := journalAccount{
		ID:          row.AccountID,
		RegistryKey: row.RegistryKey,
		Ledger:      account.Ledger,
		Code:        account.Code,
		Flags:       account.Flags,
		UserData128: row.UserData128,
		UserData64:  account.UserData64,
		UserData32:  account.UserData32,
	}
	if err := g.journal.WriteIntent(ctx, journalID, envelope); err != nil {
		return acceptedAccount{}, err
	}
	results, err := g.tb.CreateAccounts([]tb.Account{account})
	if err != nil {
		return acceptedAccount{}, fmt.Errorf("create TigerBeetle account: %w", err)
	}
	if len(results) != 1 ||
		(results[0].Status != tb.AccountCreated && results[0].Status != tb.AccountExists) {
		return acceptedAccount{}, fmt.Errorf("TigerBeetle account result: %+v", results)
	}
	outcome := journalAcceptance{
		Schema: "guardian.tigerbeetle.acceptance.v1",
		IDs:    []string{row.AccountID},
		Status: "accepted",
	}
	if err := g.journal.WriteOutcome(ctx, journalID, outcome); err != nil {
		return acceptedAccount{}, err
	}
	if err := g.queries.MarkLedgerAccountAccepted(ctx, registryKey); err != nil {
		return acceptedAccount{}, err
	}
	return acceptedAccount{ID: id}, nil
}

func journalTransfers(transfers []tb.Transfer) []journalTransfer {
	out := make([]journalTransfer, 0, len(transfers))
	for _, transfer := range transfers {
		out = append(out, journalTransfer{
			ID:              transfer.ID.String(),
			DebitAccountID:  transfer.DebitAccountID.String(),
			CreditAccountID: transfer.CreditAccountID.String(),
			Amount:          transfer.Amount.String(),
			UserData128:     transfer.UserData128.String(),
			UserData64:      transfer.UserData64,
			UserData32:      transfer.UserData32,
			Ledger:          transfer.Ledger,
			Code:            transfer.Code,
			Flags:           transfer.Flags,
		})
	}
	return out
}
