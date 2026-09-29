//go:build integration

package app_test

import (
	"fmt"
	"testing"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: HTTP-02, HTTP-04, HTTP-05
func TestQueries(t *testing.T) {
	t.Parallel()
	q := app.NewQueries(reads())
	w, p := openWallet(t, "100.00"), newProvider()
	bet := process(t, newProcessWager(), w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"})

	t.Run("wallet", func(t *testing.T) {
		got, err := q.GetWallet(t.Context(), w.ID())
		if err != nil || got.ID() != w.ID() || got.Balance().String() != "90.00" || got.Version() != 2 {
			t.Fatalf("GetWallet = %s %s v%d, %v", got.ID(), got.Balance(), got.Version(), err)
		}
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := q.GetWallet(t.Context(), id)
			wantError(t, err, apperrors.KindNotFound, "WALLET_NOT_FOUND")
		}
	})

	t.Run("transaction by id", func(t *testing.T) {
		got, err := q.GetTransaction(t.Context(), bet.Tx.ID())
		if err != nil || got.ID() != bet.Tx.ID() || got.Status() != wagering.StatusProcessed {
			t.Fatalf("GetTransaction = %v, %v", got, err)
		}
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := q.GetTransaction(t.Context(), id)
			wantError(t, err, apperrors.KindNotFound, "TRANSACTION_NOT_FOUND")
		}
	})

	t.Run("transaction by external id", func(t *testing.T) {
		got, err := q.GetTransactionByExternalID(t.Context(), p, "bet-1")
		if err != nil || got.ID() != bet.Tx.ID() {
			t.Fatalf("GetTransactionByExternalID = %v, %v", got, err)
		}
		for _, key := range [][2]string{{p, "bet-2"}, {newProvider(), "bet-1"}} {
			_, err := q.GetTransactionByExternalID(t.Context(), key[0], key[1])
			wantError(t, err, apperrors.KindNotFound, "TRANSACTION_NOT_FOUND")
		}
	})
}

// Covers: HTTP-03, LED-06
func TestListLedger(t *testing.T) {
	t.Parallel()
	q := app.NewQueries(reads())
	w, p := openWallet(t, "100.00"), newProvider()
	pw := newProcessWager()
	for i := range 4 {
		process(t, pw, w, op{provider: p, kind: "BET", amount: "1.00", ext: fmt.Sprintf("bet-%d", i)})
	}

	t.Run("pages follow the version with an opaque cursor", func(t *testing.T) {
		var versions []int64
		cursor, pages := "", 0
		for {
			page, err := q.ListLedger(t.Context(), w.ID(), cursor, 2)
			if err != nil {
				t.Fatalf("ListLedger(%q): %v", cursor, err)
			}
			pages++
			for _, e := range page.Entries {
				versions = append(versions, e.WalletVersion())
			}
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if pages != 3 || fmt.Sprint(versions) != "[1 2 3 4 5]" {
			t.Fatalf("%d pages with versions %v, want 3 pages with [1 2 3 4 5]", pages, versions)
		}
	})

	t.Run("a full last page has no next cursor", func(t *testing.T) {
		page, err := q.ListLedger(t.Context(), w.ID(), "", 5)
		if err != nil || len(page.Entries) != 5 || page.NextCursor != "" {
			t.Fatalf("ListLedger limit 5 = %d entries, next %q, %v", len(page.Entries), page.NextCursor, err)
		}
	})

	t.Run("invalid parameters are rejected before the wallet is read", func(t *testing.T) {
		_, err := q.ListLedger(t.Context(), newID(), "", 0)
		wantInvalid(t, err, wagering.InputInvalidField, "limit")
		_, err = q.ListLedger(t.Context(), newID(), "not-a-cursor", 10)
		wantInvalid(t, err, wagering.InputInvalidField, "cursor")
	})

	t.Run("unknown wallet", func(t *testing.T) {
		for _, id := range []string{newID(), "not-a-uuid"} {
			_, err := q.ListLedger(t.Context(), id, "", 10)
			wantError(t, err, apperrors.KindNotFound, "WALLET_NOT_FOUND")
		}
	})
}
