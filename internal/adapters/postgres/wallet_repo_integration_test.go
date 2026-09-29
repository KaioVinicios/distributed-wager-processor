//go:build integration

package postgres_test

import (
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// Covers: DB-01, WAL-03, DOM-02, DOM-03 (I18: wallets)
func TestWalletRepository(t *testing.T) {
	t.Parallel()

	t.Run("round trip", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		insertWallet(t, w)
		got, err := reads().Wallets().Get(ctx, w.ID())
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if walletSnapshot(t, got) != walletSnapshot(t, w) {
			t.Fatalf("Get = %+v, want %+v", walletSnapshot(t, got), walletSnapshot(t, w))
		}
		var locked wallet.Wallet
		if err := newUoW().Do(ctx, func(r app.Repos) error {
			locked, err = r.Wallets().Lock(ctx, w.ID())
			return err
		}); err != nil {
			t.Fatalf("Lock: %v", err)
		}
		if walletSnapshot(t, locked) != walletSnapshot(t, w) {
			t.Fatalf("Lock = %+v, want %+v", walletSnapshot(t, locked), walletSnapshot(t, w))
		}
	})

	t.Run("not found", func(t *testing.T) {
		ctx := t.Context()
		for _, id := range []string{newID(), "not-a-uuid", "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1"} {
			_, err := reads().Wallets().Get(ctx, id)
			wantKind(t, err, apperrors.KindNotFound, app.ErrNotFound)
		}
	})

	t.Run("second wallet for the player and currency", func(t *testing.T) {
		ctx := t.Context()
		w := zeroWallet(t)
		insertWallet(t, w)
		again, err := wallet.Open(newID(), w.PlayerID(), brl(t, "0.00"), w.CreatedAt())
		if err != nil {
			t.Fatal(err)
		}
		err = newUoW().Do(ctx, func(r app.Repos) error { return r.Wallets().Insert(ctx, again) })
		wantKind(t, err, apperrors.KindConflict, app.ErrWalletAlreadyExists)
		if code := apperrors.CodeOf(err); code != "WALLET_ALREADY_EXISTS" {
			t.Fatalf("CodeOf = %q, want WALLET_ALREADY_EXISTS", code)
		}
	})

	t.Run("zero value is rejected before writing", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Wallets().Insert(ctx, wallet.Wallet{}) })
		wantKind(t, err, apperrors.KindPermanent, wallet.ErrUninitialized)
	})

	t.Run("stored row the domain refuses", func(t *testing.T) {
		ctx := t.Context()
		id := newID()
		seedRows(t, ins("wallets", walletRow(id, newID(), 0).with("currency", "XYZ")))
		_, err := reads().Wallets().Get(ctx, id)
		wantKind(t, err, apperrors.KindPermanent, money.ErrInvalidCurrency)
	})
}
