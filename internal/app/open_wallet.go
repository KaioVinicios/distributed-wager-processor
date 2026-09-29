package app

import (
	"context"
	"errors"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// OpenWalletInput is the raw body of POST /wallets; a nil field is absent.
type OpenWalletInput struct {
	PlayerID       *string
	InitialBalance *wagering.MoneyInput
}

// OpenWallet opens a wallet (lifecycle §6.4, HTTP-01).
type OpenWallet struct {
	uow   UnitOfWork
	clock Clock
	ids   IDGenerator
}

// NewOpenWallet builds the use case.
func NewOpenWallet(uow UnitOfWork, clock Clock, ids IDGenerator) *OpenWallet {
	return &OpenWallet{uow: uow, clock: clock, ids: ids}
}

// Execute validates the input and writes, in one commit, the wallet and, for
// a positive balance, the OPENING, its CREDIT entry and its two events. A
// second wallet for (playerId, currency) is KindConflict WALLET_ALREADY_EXISTS.
func (o *OpenWallet) Execute(ctx context.Context, in OpenWalletInput, correlationID string) (wallet.Wallet, error) {
	playerID, initial, err := validateOpening(in)
	if err != nil {
		return wallet.Wallet{}, domainError(err)
	}
	opening, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: o.ids.New(), PlayerID: playerID, Initial: initial,
		TransactionID: o.ids.New(), EntryID: o.ids.New(), CorrelationID: correlationID, Now: o.clock.Now(),
	})
	if err != nil {
		return wallet.Wallet{}, domainError(err)
	}
	envs, err := sealEvents(o.ids, opening.Events, correlationID, "")
	if err != nil {
		return wallet.Wallet{}, err
	}
	err = o.uow.Do(ctx, func(r Repos) error {
		if err := r.Wallets().Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if opening.Tx == nil {
			return nil
		}
		if err := r.Transactions().Insert(ctx, opening.Tx); err != nil {
			return err
		}
		if err := r.Ledger().Insert(ctx, *opening.Entry); err != nil {
			return err
		}
		return r.Outbox().Insert(ctx, envs...)
	})
	if err != nil {
		return wallet.Wallet{}, err
	}
	return opening.Wallet, nil
}

// validateOpening checks the body in the order of lifecycle §3.1: absent
// fields first, then formats. The amount follows the strict format and may be
// zero (OPS-11).
func validateOpening(in OpenWalletInput) (string, money.Money, error) {
	missing := func(field string) (string, money.Money, error) {
		return "", money.Money{}, &wagering.ValidationError{Code: wagering.InputMissingField, Field: field}
	}
	switch {
	case in.PlayerID == nil:
		return missing("playerId")
	case in.InitialBalance == nil:
		return missing("initialBalance")
	case in.InitialBalance.Amount == nil:
		return missing("initialBalance.amount")
	case in.InitialBalance.Currency == nil:
		return missing("initialBalance.currency")
	}
	playerID, err := ident.Parse(*in.PlayerID)
	if err != nil {
		return "", money.Money{}, &wagering.ValidationError{Code: wagering.InputInvalidField, Field: "playerId"}
	}
	initial, err := money.Parse(*in.InitialBalance.Amount, *in.InitialBalance.Currency)
	switch {
	case errors.Is(err, money.ErrInvalidAmount):
		return "", money.Money{}, &wagering.ValidationError{Code: wagering.InputInvalidAmount, Field: "initialBalance.amount"}
	case err != nil:
		return "", money.Money{}, &wagering.ValidationError{Code: wagering.InputInvalidCurrency, Field: "initialBalance.currency"}
	}
	return playerID, initial, nil
}
