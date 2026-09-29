package wagering_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func openParams(t *testing.T, initial string) wagering.OpenParams {
	t.Helper()
	return wagering.OpenParams{
		WalletID: walletID, PlayerID: playerID, Initial: brl(t, initial),
		TransactionID: txID, EntryID: entryID, CorrelationID: correlation, Now: t0,
	}
}

func TestOpening(t *testing.T) {
	// Covers: TST-U06, HTTP-01, TX-04, TX-05, WAL-07, OUT-13
	o, err := wagering.OpenWallet(openParams(t, "1000.00"))
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	if o.Wallet.Version() != 1 || o.Wallet.Balance() != brl(t, "1000.00") {
		t.Fatalf("wallet %v v%d, want 1000.00 v1", o.Wallet.Balance(), o.Wallet.Version())
	}
	tx := o.Tx
	if tx == nil || tx.Kind() != wagering.KindOpening || tx.Origin() != wagering.OriginInternal ||
		tx.Status() != wagering.StatusProcessed || tx.Money() != brl(t, "1000.00") || tx.ResultBalance() != brl(t, "1000.00") ||
		tx.ProviderID() != "" || tx.IdempotencyKey() != "" || tx.PayloadHash() != "" || tx.ReceivedVia() != "" {
		t.Fatalf("OPENING = %+v", tx)
	}
	e := o.Entry
	if e == nil || e.Direction() != wallet.DirectionCredit || e.BalanceBefore() != brl(t, "0.00") ||
		e.BalanceAfter() != brl(t, "1000.00") || e.WalletVersion() != 1 || e.TransactionID() != txID {
		t.Fatalf("opening entry = %+v", e)
	}
	if got := eventTypes(o.Events); !slices.Equal(got, processedAndChanged) {
		t.Fatalf("events %v, want %v", got, processedAndChanged)
	}
	changed, ok := o.Events[1].(events.WalletBalanceChanged)
	if !ok || changed.TransactionKind != "OPENING" || changed.BalanceBefore != brl(t, "0.00") || changed.WalletVersion != 1 {
		t.Fatalf("WalletBalanceChanged = %+v", o.Events[1])
	}
	env, err := events.Seal(otherID, correlation, "", o.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"providerId", "externalTransactionId", "roundId", "gameId", "causationId"} {
		if strings.Contains(string(data), field) {
			t.Errorf("OPENING event carries the external field %s: %s", field, data)
		}
	}
	s, err := tx.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wagering.Rehydrate(s); err != nil {
		t.Fatalf("the OPENING must satisfy the persisted constraints: %v", err)
	}

	zero, err := wagering.OpenWallet(openParams(t, "0.00"))
	if err != nil || zero.Tx != nil || zero.Entry != nil || len(zero.Events) != 0 ||
		zero.Wallet.Version() != 1 || zero.Wallet.Balance().Sign() != 0 {
		t.Fatalf("zero opening: %+v, %v; want only the wallet", zero, err)
	}

	neg, err := money.FromMinor(-1, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	p := openParams(t, "1.00")
	p.Initial = neg
	if _, err := wagering.OpenWallet(p); !errors.Is(err, wallet.ErrInvalidWallet) {
		t.Errorf("negative initial balance: error = %v, want ErrInvalidWallet", err)
	}
	p = openParams(t, "1.00")
	p.EntryID = "bad"
	if _, err := wagering.OpenWallet(p); !errors.Is(err, wallet.ErrInvalidLedgerEntry) {
		t.Errorf("invalid entry id: error = %v, want ErrInvalidLedgerEntry", err)
	}
}
