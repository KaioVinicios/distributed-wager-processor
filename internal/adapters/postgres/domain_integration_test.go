//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func brl(tb testing.TB, amount string) money.Money {
	tb.Helper()
	m, err := money.Parse(amount, "BRL")
	if err != nil {
		tb.Fatalf("money %s: %v", amount, err)
	}
	return m
}

// newUoW is the unit of work over the package database, as the app role.
func newUoW() *postgres.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

// reads are the repositories over the pool, for reads outside a transaction.
func reads() app.Repos { return postgres.NewRepos(env.App) }

// zeroWallet is a new wallet with a zero opening balance: no OPENING, no entry.
func zeroWallet(tb testing.TB) wallet.Wallet {
	tb.Helper()
	w, err := wallet.Open(newID(), newID(), brl(tb, "0.00"), time.Now())
	if err != nil {
		tb.Fatalf("wallet.Open: %v", err)
	}
	return w
}

func insertWallet(tb testing.TB, w wallet.Wallet) {
	tb.Helper()
	if err := newUoW().Do(tb.Context(), func(r app.Repos) error { return r.Wallets().Insert(tb.Context(), w) }); err != nil {
		tb.Fatalf("insert wallet: %v", err)
	}
}

func walletSnapshot(tb testing.TB, w wallet.Wallet) wallet.Snapshot {
	tb.Helper()
	s, err := w.Snapshot()
	if err != nil {
		tb.Fatalf("wallet snapshot: %v", err)
	}
	return s
}

// wantKind fails unless err classifies as kind and, when target is not nil,
// wraps target.
func wantKind(tb testing.TB, err error, kind apperrors.Kind, target error) {
	tb.Helper()
	if got := apperrors.Classify(err); got != kind {
		tb.Fatalf("Classify(%v) = %q, want %q", err, got, kind)
	}
	if target != nil && !errors.Is(err, target) {
		tb.Fatalf("errors.Is(%v, %v) = false", err, target)
	}
}

// lockWallet holds the wallet's row lock in another transaction until the test
// ends or release is called.
func lockWallet(t *testing.T, walletID string) (release func()) {
	t.Helper()
	tx, err := env.Owner.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT 1 FROM wallets WHERE id = $1 FOR UPDATE`, walletID); err != nil {
		t.Fatalf("lock wallet: %v", err)
	}
	release = func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }
	t.Cleanup(release)
	return release
}

// policy is the reference schedule of the integration tests (test-plan §3.3).
var policy = func() wagering.ReferenceRetryPolicy {
	p, err := wagering.NewReferenceRetryPolicy(100*time.Millisecond, time.Second, 3, time.Minute, nil)
	if err != nil {
		panic(err)
	}
	return p
}()

// newProvider isolates the external ids of a test from the parallel ones.
func newProvider() string { return "provider-" + newID() }

func ptr(s string) *string { return &s }

// command validates an operation on w. ref "" = no reference; the currency is
// BRL unless the amount says otherwise ("10.00 USD").
func command(tb testing.TB, w wallet.Wallet, provider string, kind wagering.Kind, amount, ext, ref string) wagering.Command {
	tb.Helper()
	currency := "BRL"
	if a, c, ok := strings.Cut(amount, " "); ok {
		amount, currency = a, c
	}
	in := wagering.Input{
		IdempotencyKey: ptr(provider + ":" + ext), ProviderID: ptr(provider), ExternalTransactionID: ptr(ext),
		PlayerID: ptr(w.PlayerID()), WalletID: ptr(w.ID()), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(string(kind)), Money: &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr(currency)},
	}
	if ref != "" {
		in.ReferenceExternalTransactionID = ptr(ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		tb.Fatalf("NewCommand: %v", err)
	}
	return cmd
}

// seal wraps the domain events in envelopes, as the app does before the INSERT.
func seal(evs []events.Event, correlationID string) ([]events.Envelope, error) {
	out := make([]events.Envelope, 0, len(evs))
	for _, e := range evs {
		env, err := events.Seal(newID(), correlationID, "", e)
		if err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, nil
}

// persistOpening writes a wallet opening like POST /wallets (lifecycle §6.4).
func persistOpening(ctx context.Context, r app.Repos, o wagering.Opening) error {
	if err := r.Wallets().Insert(ctx, o.Wallet); err != nil {
		return err
	}
	if o.Tx == nil {
		return nil
	}
	if err := r.Transactions().Insert(ctx, o.Tx); err != nil {
		return err
	}
	if err := r.Ledger().Insert(ctx, *o.Entry); err != nil {
		return err
	}
	envs, err := seal(o.Events, o.Tx.CorrelationID())
	if err != nil {
		return err
	}
	return r.Outbox().Insert(ctx, envs...)
}

// openWallet opens and persists a wallet with the initial balance.
func openWallet(tb testing.TB, initial string) (wallet.Wallet, wagering.Opening) {
	tb.Helper()
	o, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: newID(), PlayerID: newID(), Initial: brl(tb, initial),
		TransactionID: newID(), EntryID: newID(), CorrelationID: "corr-open", Now: time.Now(),
	})
	if err != nil {
		tb.Fatalf("OpenWallet: %v", err)
	}
	if err := newUoW().Do(tb.Context(), func(r app.Repos) error { return persistOpening(tb.Context(), r, o) }); err != nil {
		tb.Fatalf("persist opening: %v", err)
	}
	return o.Wallet, o
}

// persistOutcome writes what Settle decided, in the order the triggers
// require: the operation is already written; then the wallet, the entry and
// the events (data-model §4.2).
func persistOutcome(ctx context.Context, r app.Repos, w wallet.Wallet, out wagering.Outcome, correlationID string) error {
	if out.Entry != nil {
		if err := r.Wallets().UpdateBalance(ctx, w); err != nil {
			return err
		}
		if err := r.Ledger().Insert(ctx, *out.Entry); err != nil {
			return err
		}
	}
	envs, err := seal(out.Events, correlationID)
	if err != nil {
		return err
	}
	return r.Outbox().Insert(ctx, envs...)
}

// processWith runs the lifecycle §6.1 pipeline of a new operation the way the
// M3 use case will: lock the wallet, resolve the reference, Settle, write.
// wrap decorates the repositories (nil = none).
func processWith(ctx context.Context, uow app.UnitOfWork, cmd wagering.Command, now time.Time, wrap func(app.Repos) app.Repos) (*wagering.WagerTransaction, error) {
	var tx *wagering.WagerTransaction
	err := uow.Do(ctx, func(r app.Repos) error {
		if wrap != nil {
			r = wrap(r)
		}
		w, err := r.Wallets().Lock(ctx, cmd.WalletID())
		if err != nil {
			return err
		}
		var ref wagering.Reference
		if cmd.ReferenceExternalTransactionID() != "" {
			if ref, err = r.Transactions().FindReference(ctx, cmd.ProviderID(), cmd.ReferenceExternalTransactionID()); err != nil {
				return err
			}
		}
		if tx, err = wagering.NewExternal(newID(), cmd, wagering.ReceivedViaHTTP, "corr-"+cmd.ExternalTransactionID(), now); err != nil {
			return err
		}
		out, err := wagering.Settle(tx, &w, ref, wagering.SettleParams{EntryID: newID(), Now: now, Policy: policy})
		if err != nil {
			return err
		}
		if err := r.Transactions().Insert(ctx, tx); err != nil {
			return err
		}
		if err := persistOutcome(ctx, r, w, out, tx.CorrelationID()); err != nil {
			return err
		}
		if tx.Status().IsTerminal() {
			_, err = r.Transactions().AdvanceDependents(ctx, cmd.ProviderID(), cmd.ExternalTransactionID(), now)
		}
		return err
	})
	return tx, err
}

// process runs processWith now and fails the test on error.
func process(tb testing.TB, cmd wagering.Command) *wagering.WagerTransaction {
	tb.Helper()
	tx, err := processWith(tb.Context(), newUoW(), cmd, time.Now(), nil)
	if err != nil {
		tb.Fatalf("process %s %s: %v", cmd.Kind(), cmd.ExternalTransactionID(), err)
	}
	return tx
}

func txSnapshot(tb testing.TB, tx *wagering.WagerTransaction) wagering.Snapshot {
	tb.Helper()
	s, err := tx.Snapshot()
	if err != nil {
		tb.Fatalf("transaction snapshot: %v", err)
	}
	return s
}

// wantStored fails unless the stored operation equals tx.
func wantStored(tb testing.TB, tx *wagering.WagerTransaction) {
	tb.Helper()
	got, err := reads().Transactions().Get(tb.Context(), tx.ID())
	if err != nil {
		tb.Fatalf("Get %s: %v", tx.ID(), err)
	}
	if g, w := txSnapshot(tb, got), txSnapshot(tb, tx); g != w {
		tb.Fatalf("stored operation differs:\n got  %+v\n want %+v", g, w)
	}
}
