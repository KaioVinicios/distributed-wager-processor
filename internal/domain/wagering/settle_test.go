package wagering_test

import (
	"errors"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

var (
	processedAndChanged = []events.Type{events.TypeWagerTransactionProcessed, events.TypeWalletBalanceChanged}
	processedOnly       = []events.Type{events.TypeWagerTransactionProcessed}
	rejectedOnly        = []events.Type{events.TypeWagerTransactionRejected}
	pendingOnly         = []events.Type{events.TypeWagerTransactionPendingReference}
)

func settle(t *testing.T, tx *wagering.WagerTransaction, w *wallet.Wallet, ref wagering.Reference) (wagering.Outcome, error) {
	t.Helper()
	return wagering.Settle(tx, w, ref, wagering.SettleParams{EntryID: entryID, Now: t0, Policy: testPolicy(t)})
}

// pendingSnapshot is a REFUND of "tx-ref" persisted as PENDING_REFERENCE.
func pendingSnapshot(t *testing.T, attempts int, expiresAt time.Time) wagering.Snapshot {
	t.Helper()
	tx := external(t, wagering.KindRefund, "25.00", refExtID)
	if _, err := tx.AwaitReference(t0, testPolicy(t)); err != nil {
		t.Fatal(err)
	}
	s, err := tx.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	s.Attempts, s.ExpiresAt = attempts, expiresAt
	return s
}

func TestKindRules(t *testing.T) {
	// Covers: TST-U04, OPS-01, OPS-02, OPS-03, OPS-04, OPS-05, OPS-10, WAL-06, WAL-07, LED-05
	tests := []struct {
		name          string
		kind          wagering.Kind
		amount        string
		refKind       wagering.Kind // "" = no reference
		refAmount     string
		wantStatus    wagering.Status
		wantCode      wagering.FailureCode
		wantDirection wallet.Direction // "" = no ledger entry
		wantBalance   string
		wantVersion   int64
		wantEvents    []events.Type
	}{
		{"BET debits", wagering.KindBet, "25.00", "", "", wagering.StatusProcessed, "", wallet.DirectionDebit, "75.00", 2, processedAndChanged},
		{"BET without funds", wagering.KindBet, "100.01", "", "", wagering.StatusRejected, wagering.FailureInsufficientFunds, "", "100.00", 1, rejectedOnly},
		{"BET of the whole balance", wagering.KindBet, "100.00", "", "", wagering.StatusProcessed, "", wallet.DirectionDebit, "0.00", 2, processedAndChanged},
		{"WIN credits", wagering.KindWin, "40.00", "", "", wagering.StatusProcessed, "", wallet.DirectionCredit, "140.00", 2, processedAndChanged},
		{"WIN of a BET, other amount", wagering.KindWin, "40.00", wagering.KindBet, "25.00", wagering.StatusProcessed, "", wallet.DirectionCredit, "140.00", 2, processedAndChanged},
		{"LOSS moves nothing", wagering.KindLoss, "0.00", "", "", wagering.StatusProcessed, "", "", "100.00", 1, processedOnly},
		{"REFUND of a BET credits", wagering.KindRefund, "25.00", wagering.KindBet, "25.00", wagering.StatusProcessed, "", wallet.DirectionCredit, "125.00", 2, processedAndChanged},
		{"ROLLBACK of a BET credits", wagering.KindRollback, "25.00", wagering.KindBet, "25.00", wagering.StatusProcessed, "", wallet.DirectionCredit, "125.00", 2, processedAndChanged},
		{"ROLLBACK of a WIN debits", wagering.KindRollback, "40.00", wagering.KindWin, "40.00", wagering.StatusProcessed, "", wallet.DirectionDebit, "60.00", 2, processedAndChanged},
		{"ROLLBACK of a REFUND debits", wagering.KindRollback, "25.00", wagering.KindRefund, "25.00", wagering.StatusProcessed, "", wallet.DirectionDebit, "75.00", 2, processedAndChanged},
		{"ROLLBACK without funds", wagering.KindRollback, "150.00", wagering.KindWin, "150.00", wagering.StatusRejected, wagering.FailureReversalInsufficientFunds, "", "100.00", 1, rejectedOnly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ref := wagering.Reference{}
			refExt := ""
			if tc.refKind != "" {
				refExt = refExtID
				ref.Tx = rehydrate(t, referenceSnapshot(t, tc.refKind, tc.refAmount))
			}
			tx := external(t, tc.kind, tc.amount, refExt)
			w := openWallet(t, "100.00")

			out, err := settle(t, tx, &w, ref)
			if err != nil {
				t.Fatalf("Settle: %v", err)
			}
			if tx.Status() != tc.wantStatus || tx.FailureCode() != tc.wantCode {
				t.Fatalf("status %s %s, want %s %s", tx.Status(), tx.FailureCode(), tc.wantStatus, tc.wantCode)
			}
			if w.Balance() != brl(t, tc.wantBalance) || w.Version() != tc.wantVersion {
				t.Fatalf("wallet %v v%d, want %s v%d", w.Balance(), w.Version(), tc.wantBalance, tc.wantVersion)
			}
			if tx.ResultBalance() != w.Balance() {
				t.Fatalf("result balance %v, want the observed %v", tx.ResultBalance(), w.Balance())
			}
			switch {
			case tc.wantDirection == "" && out.Entry != nil:
				t.Fatalf("unexpected ledger entry %+v", out.Entry)
			case tc.wantDirection != "" && (out.Entry == nil || out.Entry.Direction() != tc.wantDirection ||
				out.Entry.TransactionID() != tx.ID() || out.Entry.WalletVersion() != w.Version()):
				t.Fatalf("entry %+v, want %s at version %d", out.Entry, tc.wantDirection, w.Version())
			}
			if got := eventTypes(out.Events); !slices.Equal(got, tc.wantEvents) {
				t.Fatalf("events %v, want %v", got, tc.wantEvents)
			}
			if tc.refKind != "" && tx.Status() == wagering.StatusProcessed && tx.ReferenceTransactionID() != refTxID {
				t.Fatalf("reference id %q, want %q", tx.ReferenceTransactionID(), refTxID)
			}
		})
	}
}

func TestSettleEvaluationOrder(t *testing.T) {
	// Covers: OPS-15, OPS-07
	usd := func(amount string) money.Money {
		m, err := money.Parse(amount, "USD")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	tests := map[string]struct {
		tx     *wagering.WagerTransaction
		wallet func() wallet.Wallet
		ref    func() wagering.Reference
		want   wagering.FailureCode
	}{
		"player before currency": {
			external(t, wagering.KindBet, "25.00", ""),
			func() wallet.Wallet { w, _ := wallet.Open(walletID, otherID, usd("100.00"), t0); return w },
			func() wagering.Reference { return wagering.Reference{} },
			wagering.FailurePlayerWalletMismatch,
		},
		"currency before the reference": {
			external(t, wagering.KindRefund, "25.00", refExtID),
			func() wallet.Wallet { w, _ := wallet.Open(walletID, playerID, usd("100.00"), t0); return w },
			func() wagering.Reference { return wagering.Reference{} },
			wagering.FailureCurrencyMismatch,
		},
		"reference before funds": {
			external(t, wagering.KindRollback, "150.00", refExtID),
			func() wallet.Wallet { return openWallet(t, "100.00") },
			func() wagering.Reference {
				s := referenceSnapshot(t, wagering.KindWin, "150.00")
				s.RoundID = "round-000"
				return wagering.Reference{Tx: rehydrate(t, s)}
			},
			wagering.FailureReferenceMismatch,
		},
	}
	for name, tc := range tests {
		w := tc.wallet()
		out, err := settle(t, tc.tx, &w, tc.ref())
		if err != nil || tc.tx.FailureCode() != tc.want || !slices.Equal(eventTypes(out.Events), rejectedOnly) {
			t.Errorf("%s: %s, %v; want %s", name, tc.tx.FailureCode(), err, tc.want)
		}
		if tc.tx.ResultBalance() != w.Balance() {
			t.Errorf("%s: result balance %v, want the wallet balance %v", name, tc.tx.ResultBalance(), w.Balance())
		}
	}
}

func TestSettleUnresolvedReference(t *testing.T) {
	// Covers: TST-C07, OPS-12, OPS-13, OPS-14
	w := openWallet(t, "100.00")

	// First pass (HTTP/SQS): the reference is missing.
	tx := external(t, wagering.KindRefund, "25.00", refExtID)
	out, err := settle(t, tx, &w, wagering.Reference{})
	if err != nil || tx.Status() != wagering.StatusPendingReference || out.Entry != nil ||
		!slices.Equal(eventTypes(out.Events), pendingOnly) || w.Version() != 1 {
		t.Fatalf("R1: %s %v %v", tx.Status(), out, err)
	}

	// Worker, still missing: one more attempt, no event.
	out, err = settle(t, tx, &w, wagering.Reference{})
	if err != nil || tx.Status() != wagering.StatusPendingReference || tx.Attempts() != 1 || len(out.Events) != 0 {
		t.Fatalf("reschedule: %s attempts %d events %v err %v", tx.Status(), tx.Attempts(), out.Events, err)
	}

	// Worker, the BET arrived: PROCESSED from PENDING_REFERENCE.
	bet := rehydrate(t, referenceSnapshot(t, wagering.KindBet, "25.00"))
	out, err = settle(t, tx, &w, wagering.Reference{Tx: bet})
	if err != nil || tx.Status() != wagering.StatusProcessed || !slices.Equal(eventTypes(out.Events), processedAndChanged) ||
		w.Balance() != brl(t, "125.00") {
		t.Fatalf("resolution: %s %v %v, balance %v", tx.Status(), out.Events, err, w.Balance())
	}

	// Worker: the reference ended REJECTED while this one was waiting.
	waiting := rehydrate(t, pendingSnapshot(t, 1, utc(t0).Add(time.Hour)))
	failedRef := referenceSnapshot(t, wagering.KindBet, "25.00")
	failedRef.Status, failedRef.FailureCode = wagering.StatusRejected, wagering.FailureInsufficientFunds
	w2 := openWallet(t, "100.00")
	out, err = settle(t, waiting, &w2, wagering.Reference{Tx: rehydrate(t, failedRef)})
	if err != nil || waiting.Status() != wagering.StatusRejected || waiting.FailureCode() != wagering.FailureReferenceNotProcessed ||
		!slices.Equal(eventTypes(out.Events), rejectedOnly) {
		t.Fatalf("reference rejected meanwhile: %s %s %v %v", waiting.Status(), waiting.FailureCode(), out.Events, err)
	}

	expired := map[string]wagering.Snapshot{
		"attempts exhausted":              pendingSnapshot(t, 7, utc(t0).Add(time.Hour)),
		"TTL passed on the first attempt": pendingSnapshot(t, 0, utc(t0).Add(-time.Second)),
	}
	for name, s := range expired {
		tx := rehydrate(t, s)
		w := openWallet(t, "100.00")
		out, err := settle(t, tx, &w, wagering.Reference{})
		if err != nil || tx.Status() != wagering.StatusRejected || tx.FailureCode() != wagering.FailureReferenceNotFound ||
			!slices.Equal(eventTypes(out.Events), rejectedOnly) || tx.ResultBalance() != brl(t, "100.00") {
			t.Errorf("%s: %s %s %v %v", name, tx.Status(), tx.FailureCode(), out.Events, err)
		}
	}
}

func TestSettlePreconditions(t *testing.T) {
	// Covers: DOM-01, DOM-04, DOM-05, E5
	processed := rehydrate(t, referenceSnapshot(t, wagering.KindBet, "25.00"))
	opening, _ := wagering.NewOpening(txID, walletID, playerID, brl(t, "1.00"), correlation, t0)
	otherWallet, _ := wallet.Open(otherID, playerID, brl(t, "100.00"), t0)
	foreignRef := referenceSnapshot(t, wagering.KindBet, "25.00")
	foreignRef.ProviderID = "provider-b"

	tests := map[string]struct {
		tx     *wagering.WagerTransaction
		wallet *wallet.Wallet
		ref    wagering.Reference
		want   error
	}{
		"already PROCESSED (no double movement)": {processed, ptrWallet(openWallet(t, "100.00")), wagering.Reference{}, wagering.ErrInvalidTransition},
		"opening":                                {opening, ptrWallet(openWallet(t, "100.00")), wagering.Reference{}, wagering.ErrInvalidTransition},
		"nil wallet":                             {external(t, wagering.KindBet, "1.00", ""), nil, wagering.Reference{}, wagering.ErrUninitialized},
		"zero wallet":                            {external(t, wagering.KindBet, "1.00", ""), &wallet.Wallet{}, wagering.Reference{}, wagering.ErrUninitialized},
		"another wallet":                         {external(t, wagering.KindBet, "1.00", ""), &otherWallet, wagering.Reference{}, wagering.ErrInvalidArgument},
		"reference of another provider": {
			external(t, wagering.KindRefund, "25.00", refExtID), ptrWallet(openWallet(t, "100.00")),
			wagering.Reference{Tx: rehydrate(t, foreignRef)},
			wagering.ErrInvalidArgument,
		},
		"reference given to a BET": {
			external(t, wagering.KindBet, "1.00", ""), ptrWallet(openWallet(t, "100.00")),
			wagering.Reference{Tx: processed},
			wagering.ErrInvalidArgument,
		},
		"zero transaction": {&wagering.WagerTransaction{}, ptrWallet(openWallet(t, "100.00")), wagering.Reference{}, wagering.ErrUninitialized},
	}
	for name, tc := range tests {
		var before wallet.Wallet
		if tc.wallet != nil {
			before = *tc.wallet
		}
		status := tc.tx.Status()
		if _, err := settle(t, tc.tx, tc.wallet, tc.ref); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", name, err, tc.want)
		}
		if tc.tx.Status() != status || (tc.wallet != nil && *tc.wallet != before) {
			t.Errorf("%s: a rejected precondition changed the state", name)
		}
	}

	// An invariant broken during the movement leaves both sides untouched.
	tx := external(t, wagering.KindBet, "25.00", "")
	w := openWallet(t, "100.00")
	before := w
	_, err := wagering.Settle(tx, &w, wagering.Reference{}, wagering.SettleParams{EntryID: "bad", Now: t0, Policy: testPolicy(t)})
	if !errors.Is(err, wallet.ErrInvalidLedgerEntry) || tx.Status() != wagering.StatusPending || w != before {
		t.Fatalf("invalid entry id: %v, status %s, wallet changed %v", err, tx.Status(), w != before)
	}
	refund := external(t, wagering.KindRefund, "25.00", refExtID)
	_, err = wagering.Settle(refund, &w, wagering.Reference{}, wagering.SettleParams{EntryID: entryID, Now: t0})
	if !errors.Is(err, wagering.ErrInvalidArgument) || refund.Status() != wagering.StatusPending {
		t.Fatalf("zero policy: %v, status %s", err, refund.Status())
	}
}

func ptrWallet(w wallet.Wallet) *wallet.Wallet { return &w }

func TestSettleEdgeCases(t *testing.T) {
	// Covers: MON-08, WAL-07, OPS-03
	// A credit that would overflow int64 is a permanent error, not a
	// rejection, and nothing changes.
	full, err := wallet.Rehydrate(wallet.Snapshot{
		ID: walletID, PlayerID: playerID, Balance: mustMinor(t, math.MaxInt64-100), Version: 4, CreatedAt: t0, UpdatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	win := external(t, wagering.KindWin, "40.00", "")
	before := full
	if _, err := settle(t, win, &full, wagering.Reference{}); !errors.Is(err, money.ErrOverflow) ||
		win.Status() != wagering.StatusPending || full != before {
		t.Fatalf("overflowing WIN: %v, status %s, wallet changed %v", err, win.Status(), full != before)
	}

	// LOSS reports the current version and balance, without incrementing.
	w, err := wallet.Rehydrate(wallet.Snapshot{
		ID: walletID, PlayerID: playerID, Balance: brl(t, "50.00"), Version: 3, CreatedAt: t0, UpdatedAt: t0,
	})
	if err != nil {
		t.Fatal(err)
	}
	loss := external(t, wagering.KindLoss, "0.00", "")
	out, err := settle(t, loss, &w, wagering.Reference{})
	if err != nil || len(out.Events) != 1 {
		t.Fatalf("LOSS: %v, %d events", err, len(out.Events))
	}
	processed, ok := out.Events[0].(events.WagerTransactionProcessed)
	if !ok || processed.WalletVersion != 3 || processed.BalanceAfter != brl(t, "50.00") || w.Version() != 3 ||
		loss.ResultBalance() != brl(t, "50.00") {
		t.Fatalf("LOSS event %+v, wallet v%d", processed, w.Version())
	}
}

func mustMinor(t *testing.T, v int64) money.Money {
	t.Helper()
	m, err := money.FromMinor(v, money.BRL)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
