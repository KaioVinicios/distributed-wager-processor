package wallet_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func validEntry(t *testing.T) wallet.LedgerEntryParams {
	t.Helper()
	return wallet.LedgerEntryParams{
		ID: entryID, WalletID: walletID, TransactionID: txID, Direction: wallet.DirectionDebit,
		Amount: brl(t, "25.00"), BalanceBefore: brl(t, "100.00"), BalanceAfter: brl(t, "75.00"),
		WalletVersion: 2, CreatedAt: t0,
	}
}

func TestLedgerEntryInvariant(t *testing.T) {
	// Covers: TST-U02, LED-01, LED-02
	e, err := wallet.NewLedgerEntry(validEntry(t))
	if err != nil {
		t.Fatalf("valid debit: %v", err)
	}
	if e.ID() != entryID || e.WalletID() != walletID || e.TransactionID() != txID ||
		e.Direction() != wallet.DirectionDebit || e.Amount() != brl(t, "25.00") ||
		e.BalanceBefore() != brl(t, "100.00") || e.BalanceAfter() != brl(t, "75.00") || e.WalletVersion() != 2 {
		t.Fatalf("getters do not return the params: %+v", e)
	}
	if want := t0.UTC().Truncate(time.Microsecond); !e.CreatedAt().Equal(want) || e.CreatedAt().Location() != time.UTC {
		t.Fatalf("CreatedAt = %v, want %v in UTC", e.CreatedAt(), want)
	}

	credit := validEntry(t)
	credit.Direction, credit.BalanceAfter = wallet.DirectionCredit, brl(t, "125.00")
	if _, err := wallet.NewLedgerEntry(credit); err != nil {
		t.Fatalf("valid credit: %v", err)
	}

	broken := map[string]func(p *wallet.LedgerEntryParams){
		"debit with credit math": func(p *wallet.LedgerEntryParams) { p.BalanceAfter = brl(t, "125.00") },
		"credit with debit math": func(p *wallet.LedgerEntryParams) { p.Direction = wallet.DirectionCredit },
		"zero amount":            func(p *wallet.LedgerEntryParams) { p.Amount, p.BalanceAfter = brl(t, "0.00"), brl(t, "100.00") },
		"uninitialized amount":   func(p *wallet.LedgerEntryParams) { p.Amount = money.Money{} },
		"negative balance after": func(p *wallet.LedgerEntryParams) {
			p.BalanceBefore, p.BalanceAfter = brl(t, "10.00"), minor(t, -1500, money.BRL)
		},
		"currency mismatch": func(p *wallet.LedgerEntryParams) { p.BalanceAfter = minor(t, 7500, money.USD) },
		"overflowing credit": func(p *wallet.LedgerEntryParams) {
			p.Direction, p.BalanceBefore = wallet.DirectionCredit, minor(t, math.MaxInt64, money.BRL)
		},
		"unknown direction":      func(p *wallet.LedgerEntryParams) { p.Direction = "SIDEWAYS" },
		"version zero":           func(p *wallet.LedgerEntryParams) { p.WalletVersion = 0 },
		"invalid id":             func(p *wallet.LedgerEntryParams) { p.ID = "entry-1" },
		"invalid wallet id":      func(p *wallet.LedgerEntryParams) { p.WalletID = "" },
		"invalid transaction id": func(p *wallet.LedgerEntryParams) { p.TransactionID = "TX" },
		"zero created at":        func(p *wallet.LedgerEntryParams) { p.CreatedAt = time.Time{} },
	}
	for name, mutate := range broken {
		p := validEntry(t)
		mutate(&p)
		if _, err := wallet.NewLedgerEntry(p); !errors.Is(err, wallet.ErrInvalidLedgerEntry) {
			t.Errorf("%s: error = %v, want ErrInvalidLedgerEntry", name, err)
		}
	}
}
