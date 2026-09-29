//go:build integration

package app_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: HTTP-01, WAL-03, OUT-13, TST-U06 (I20)
func TestOpenWallet(t *testing.T) {
	t.Parallel()

	t.Run("positive balance opens with OPENING, entry and events in one commit", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		player := newID()
		w, err := newOpenWallet().Execute(ctx, app.OpenWalletInput{
			PlayerID: ptr(player), InitialBalance: moneyInput("100.00", "BRL"),
		}, "corr-open-1")
		if err != nil {
			t.Fatalf("Execute: %v", err)
		}
		if w.PlayerID() != player || w.Balance().String() != "100.00" || w.Version() != 1 {
			t.Fatalf("wallet = %s %s v%d", w.PlayerID(), w.Balance(), w.Version())
		}
		stored, err := reads().Wallets().Get(ctx, w.ID())
		if err != nil || stored != w {
			t.Fatalf("stored wallet = %+v, %v; want %+v", stored, err, w)
		}
		if n := count(t, `SELECT count(*) FROM wager_transactions
			WHERE wallet_id = $1 AND kind = 'OPENING' AND origin = 'INTERNAL' AND status = 'PROCESSED'`, w.ID()); n != 1 {
			t.Fatalf("%d OPENING operations, want 1", n)
		}
		sum, err := reads().Ledger().Sum(ctx, w.ID())
		if err != nil || sum.Entries != 1 || sum.NetMinor != 10000 {
			t.Fatalf("ledger = %+v, %v; want 1 credit of 100.00", sum, err)
		}
		types, corrs := outboxTypes(t, w.ID())
		if strings.Join(types, ",") != "WagerTransactionProcessed,WalletBalanceChanged" ||
			corrs[0] != "corr-open-1" || corrs[1] != "corr-open-1" {
			t.Fatalf("outbox = %v %v", types, corrs)
		}
		trackWallet(t, w.ID())
	})

	t.Run("zero balance opens only the wallet", func(t *testing.T) {
		t.Parallel()
		w := openWallet(t, "0.00")
		if w.Balance().String() != "0.00" || w.Version() != 1 {
			t.Fatalf("wallet = %s v%d", w.Balance(), w.Version())
		}
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE wallet_id = $1`, w.ID()); n != 0 {
			t.Fatalf("%d operations, want none", n)
		}
		if types, _ := outboxTypes(t, w.ID()); len(types) != 0 {
			t.Fatalf("outbox = %v, want empty", types)
		}
	})

	t.Run("second wallet for the player and currency conflicts", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		player := newID()
		in := app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: moneyInput("10.00", "BRL")}
		first, err := newOpenWallet().Execute(ctx, in, "corr")
		if err != nil {
			t.Fatalf("first: %v", err)
		}
		trackWallet(t, first.ID())

		_, err = newOpenWallet().Execute(ctx, in, "corr")
		wantError(t, err, apperrors.KindConflict, "WALLET_ALREADY_EXISTS")
		if !errors.Is(err, app.ErrWalletAlreadyExists) {
			t.Fatalf("errors.Is(%v, ErrWalletAlreadyExists) = false", err)
		}
		other, err := newOpenWallet().Execute(ctx, app.OpenWalletInput{
			PlayerID: ptr(strings.ToUpper(player)), InitialBalance: moneyInput("0.00", "USD"),
		}, "corr")
		if err != nil || other.PlayerID() != player {
			t.Fatalf("USD wallet for the same player = %s, %v; want the lowercase player id", other.PlayerID(), err)
		}
	})

	t.Run("invalid input is rejected before writing", func(t *testing.T) {
		t.Parallel()
		player := newID()
		cases := []struct {
			name  string
			in    app.OpenWalletInput
			code  wagering.InputCode
			field string
		}{
			{"missing player", app.OpenWalletInput{InitialBalance: moneyInput("1.00", "BRL")}, wagering.InputMissingField, "playerId"},
			{"missing balance", app.OpenWalletInput{PlayerID: ptr(player)}, wagering.InputMissingField, "initialBalance"},
			{"missing amount", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: &wagering.MoneyInput{Currency: ptr("BRL")}}, wagering.InputMissingField, "initialBalance.amount"},
			{"missing currency", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: &wagering.MoneyInput{Amount: ptr("1.00")}}, wagering.InputMissingField, "initialBalance.currency"},
			{"missing wins over invalid", app.OpenWalletInput{PlayerID: ptr("not-a-uuid")}, wagering.InputMissingField, "initialBalance"},
			{"invalid player", app.OpenWalletInput{PlayerID: ptr("not-a-uuid"), InitialBalance: moneyInput("1.00", "BRL")}, wagering.InputInvalidField, "playerId"},
			{"negative amount", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: moneyInput("-1.00", "BRL")}, wagering.InputInvalidAmount, "initialBalance.amount"},
			{"amount without decimals", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: moneyInput("10", "BRL")}, wagering.InputInvalidAmount, "initialBalance.amount"},
			{"lowercase currency", app.OpenWalletInput{PlayerID: ptr(player), InitialBalance: moneyInput("1.00", "brl")}, wagering.InputInvalidCurrency, "initialBalance.currency"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := newOpenWallet().Execute(t.Context(), tc.in, "corr")
				wantInvalid(t, err, tc.code, tc.field)
			})
		}
		if n := count(t, `SELECT count(*) FROM wallets WHERE player_id = $1`, player); n != 0 {
			t.Fatalf("%d wallets written for invalid input", n)
		}
	})
}
