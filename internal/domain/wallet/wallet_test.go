package wallet_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func open(t *testing.T, initial string) wallet.Wallet {
	t.Helper()
	w, err := wallet.Open(walletID, playerID, brl(t, initial), t0)
	if err != nil {
		t.Fatalf("Open(%s): %v", initial, err)
	}
	return w
}

func TestWalletOpen(t *testing.T) {
	// Covers: TST-U02, WAL-01, WAL-02, WAL-07
	w := open(t, "100.00")
	want := t0.UTC().Truncate(time.Microsecond)
	if w.ID() != walletID || w.PlayerID() != playerID || w.Currency() != money.BRL ||
		w.Balance() != brl(t, "100.00") || w.Version() != 1 ||
		!w.CreatedAt().Equal(want) || !w.UpdatedAt().Equal(want) || w.CreatedAt().Location() != time.UTC {
		t.Fatalf("Open = %+v", w)
	}
	if z := open(t, "0.00"); z.Version() != 1 || z.Balance().Sign() != 0 {
		t.Fatalf("zero initial balance: version %d balance %v", z.Version(), z.Balance())
	}

	invalid := map[string]func() (wallet.Wallet, error){
		"negative initial": func() (wallet.Wallet, error) {
			return wallet.Open(walletID, playerID, minor(t, -1, money.BRL), t0)
		},
		"uninitialized initial": func() (wallet.Wallet, error) { return wallet.Open(walletID, playerID, money.Money{}, t0) },
		"invalid id":            func() (wallet.Wallet, error) { return wallet.Open("w-1", playerID, brl(t, "1.00"), t0) },
		"uppercase player id": func() (wallet.Wallet, error) {
			return wallet.Open(walletID, "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1", brl(t, "1.00"), t0)
		},
		"zero now": func() (wallet.Wallet, error) { return wallet.Open(walletID, playerID, brl(t, "1.00"), time.Time{}) },
	}
	for name, op := range invalid {
		if _, err := op(); !errors.Is(err, wallet.ErrInvalidWallet) {
			t.Errorf("%s: error = %v, want ErrInvalidWallet", name, err)
		}
	}
}

func TestWalletDebit(t *testing.T) {
	// Covers: TST-U02, WAL-02, WAL-04, WAL-05, WAL-07
	w := open(t, "100.00")
	later := t0.Add(time.Minute)
	e, err := w.Debit(entryID, txID, brl(t, "30.00"), later)
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if w.Balance() != brl(t, "70.00") || w.Version() != 2 || !w.UpdatedAt().Equal(later.Truncate(time.Microsecond)) {
		t.Fatalf("after debit: balance %v version %d updated %v", w.Balance(), w.Version(), w.UpdatedAt())
	}
	if e.Direction() != wallet.DirectionDebit || e.BalanceBefore() != brl(t, "100.00") ||
		e.BalanceAfter() != brl(t, "70.00") || e.WalletVersion() != 2 || e.TransactionID() != txID {
		t.Fatalf("entry = %+v", e)
	}

	if _, err := w.Debit(entryID, txID, brl(t, "70.00"), later); err != nil || w.Balance().Sign() != 0 {
		t.Fatalf("debit of the whole balance: %v, balance %v", err, w.Balance())
	}

	skewed := open(t, "10.00")
	if _, err := skewed.Debit(entryID, txID, brl(t, "1.00"), t0.Add(-time.Second)); err != nil ||
		!skewed.UpdatedAt().Equal(skewed.CreatedAt()) {
		t.Fatalf("clock behind the creation: %v, updated %v, created %v", err, skewed.UpdatedAt(), skewed.CreatedAt())
	}

	before := w
	failures := map[string]struct {
		amount money.Money
		want   error
	}{
		"more than the balance": {brl(t, "0.01"), wallet.ErrInsufficientFunds},
		"zero":                  {brl(t, "0.00"), wallet.ErrInvalidAmount},
		"uninitialized":         {money.Money{}, wallet.ErrInvalidAmount},
		"other currency":        {minor(t, 1, money.USD), money.ErrCurrencyMismatch},
	}
	for name, f := range failures {
		if _, err := w.Debit(entryID, txID, f.amount, later); !errors.Is(err, f.want) {
			t.Errorf("%s: error = %v, want %v", name, err, f.want)
		}
		if w != before {
			t.Fatalf("%s: a failed debit changed the wallet", name)
		}
	}
}

func TestWalletCredit(t *testing.T) {
	// Covers: TST-U02, WAL-02, WAL-05, WAL-07, MON-08
	w := open(t, "10.00")
	e, err := w.Credit(entryID, txID, brl(t, "5.50"), t0)
	if err != nil || w.Balance() != brl(t, "15.50") || w.Version() != 2 {
		t.Fatalf("Credit: %v, balance %v, version %d", err, w.Balance(), w.Version())
	}
	if e.Direction() != wallet.DirectionCredit || e.BalanceBefore() != brl(t, "10.00") || e.BalanceAfter() != brl(t, "15.50") {
		t.Fatalf("entry = %+v", e)
	}

	full, err := wallet.Rehydrate(wallet.Snapshot{
		ID: walletID, PlayerID: playerID, Balance: minor(t, math.MaxInt64, money.BRL),
		Version: 7, CreatedAt: t0, UpdatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := full
	if _, err := full.Credit(entryID, txID, brl(t, "0.01"), t0); !errors.Is(err, money.ErrOverflow) {
		t.Fatalf("overflowing credit: error = %v, want ErrOverflow", err)
	}
	if full != before {
		t.Fatal("a failed credit changed the wallet")
	}
	if _, err := w.Credit("bad-id", txID, brl(t, "1.00"), t0); !errors.Is(err, wallet.ErrInvalidLedgerEntry) || w.Version() != 2 {
		t.Fatalf("credit with an invalid entry id: %v, version %d", err, w.Version())
	}
}

func TestWalletRehydrate(t *testing.T) {
	// Covers: TST-U02, DOM-02, WAL-01
	s := wallet.Snapshot{
		ID: walletID, PlayerID: playerID, Balance: brl(t, "42.00"), Version: 9,
		CreatedAt: t0.UTC().Truncate(time.Microsecond), UpdatedAt: t0.UTC().Truncate(time.Microsecond).Add(time.Hour),
	}
	w, err := wallet.Rehydrate(s)
	if err != nil {
		t.Fatalf("Rehydrate: %v", err)
	}
	got, err := w.Snapshot()
	if err != nil || got != s {
		t.Fatalf("round trip: %+v, %v; want %+v (no transition, same version)", got, err, s)
	}

	corrupt := map[string]func(s *wallet.Snapshot){
		"negative balance":       func(s *wallet.Snapshot) { s.Balance = minor(t, -1, money.BRL) },
		"uninitialized balance":  func(s *wallet.Snapshot) { s.Balance = money.Money{} },
		"version zero":           func(s *wallet.Snapshot) { s.Version = 0 },
		"invalid id":             func(s *wallet.Snapshot) { s.ID = "x" },
		"invalid player":         func(s *wallet.Snapshot) { s.PlayerID = "" },
		"updated before created": func(s *wallet.Snapshot) { s.UpdatedAt = s.CreatedAt.Add(-time.Second) },
		"zero created at":        func(s *wallet.Snapshot) { s.CreatedAt, s.UpdatedAt = time.Time{}, time.Time{} },
	}
	for name, mutate := range corrupt {
		c := s
		mutate(&c)
		if _, err := wallet.Rehydrate(c); !errors.Is(err, wallet.ErrInvalidWallet) {
			t.Errorf("%s: error = %v, want ErrInvalidWallet", name, err)
		}
	}
}

func TestWalletOpeningEntry(t *testing.T) {
	// Covers: TST-U06, HTTP-01, WAL-07
	w := open(t, "1000.00")
	e, err := w.OpeningEntry(entryID, txID)
	if err != nil {
		t.Fatalf("OpeningEntry: %v", err)
	}
	if e.Direction() != wallet.DirectionCredit || e.BalanceBefore() != brl(t, "0.00") ||
		e.BalanceAfter() != brl(t, "1000.00") || e.Amount() != brl(t, "1000.00") ||
		e.WalletVersion() != 1 || !e.CreatedAt().Equal(w.CreatedAt()) {
		t.Fatalf("opening entry = %+v", e)
	}
	if w.Version() != 1 || w.Balance() != brl(t, "1000.00") {
		t.Fatal("OpeningEntry must not change the wallet")
	}

	zero := open(t, "0.00")
	if _, err := zero.OpeningEntry(entryID, txID); !errors.Is(err, wallet.ErrInvalidOpening) {
		t.Errorf("zero balance: error = %v, want ErrInvalidOpening", err)
	}
	if _, err := w.Debit(entryID, txID, brl(t, "1.00"), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.OpeningEntry(entryID, txID); !errors.Is(err, wallet.ErrInvalidOpening) {
		t.Errorf("version 2: error = %v, want ErrInvalidOpening", err)
	}
}

func TestZeroValuesRejected(t *testing.T) {
	// Covers: DOM-03
	var z wallet.Wallet
	var nilWallet *wallet.Wallet
	ops := map[string]func() error{
		"Debit":        func() error { _, err := z.Debit(entryID, txID, brl(t, "1.00"), t0); return err },
		"Credit":       func() error { _, err := z.Credit(entryID, txID, brl(t, "1.00"), t0); return err },
		"nil Debit":    func() error { _, err := nilWallet.Debit(entryID, txID, brl(t, "1.00"), t0); return err },
		"nil Credit":   func() error { _, err := nilWallet.Credit(entryID, txID, brl(t, "1.00"), t0); return err },
		"OpeningEntry": func() error { _, err := z.OpeningEntry(entryID, txID); return err },
		"Snapshot":     func() error { _, err := z.Snapshot(); return err },
	}
	for name, op := range ops {
		if err := op(); !errors.Is(err, wallet.ErrUninitialized) {
			t.Errorf("Wallet{}.%s: error = %v, want ErrUninitialized", name, err)
		}
	}
}
