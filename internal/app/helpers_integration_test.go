//go:build integration

package app_test

import (
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/test/testkit"
)

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func ptr(s string) *string { return &s }

// newUoW is the unit of work over the package database, as the app role.
func newUoW() app.UnitOfWork { return postgres.NewUnitOfWork(env.App, env.Config()) }

// reads are the repositories over the pool, for reads outside a transaction.
func reads() app.Repos { return postgres.NewRepos(env.App) }

func newOpenWallet() *app.OpenWallet {
	return app.NewOpenWallet(newUoW(), app.SystemClock{}, app.UUIDv7{})
}

func moneyInput(amount, currency string) *wagering.MoneyInput {
	return &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr(currency)}
}

// openWallet opens a BRL wallet through the use case and checks the ledger of
// test-plan §6 when the test ends.
func openWallet(t *testing.T, initial string) wallet.Wallet {
	t.Helper()
	w, err := newOpenWallet().Execute(t.Context(), app.OpenWalletInput{
		PlayerID: ptr(newID()), InitialBalance: moneyInput(initial, "BRL"),
	}, "corr-open")
	if err != nil {
		t.Fatalf("OpenWallet %s: %v", initial, err)
	}
	trackWallet(t, w.ID())
	return w
}

// trackWallet checks the wallet against test-plan §6 when the test ends.
func trackWallet(t *testing.T, walletID string) {
	t.Helper()
	t.Cleanup(func() { testkit.AssertLedgerConsistent(t, env.Owner, walletID) })
}

// wantError fails unless err classifies as kind with code.
func wantError(t *testing.T, err error, kind apperrors.Kind, code string) {
	t.Helper()
	if got := apperrors.Classify(err); got != kind || apperrors.CodeOf(err) != code {
		t.Fatalf("error = %v (%s %q), want %s %q", err, got, apperrors.CodeOf(err), kind, code)
	}
}

// wantInvalid fails unless err is the validation error code on field.
func wantInvalid(t *testing.T, err error, code wagering.InputCode, field string) {
	t.Helper()
	var ve *wagering.ValidationError
	if apperrors.Classify(err) != apperrors.KindInput || !errors.As(err, &ve) || ve.Code != code || ve.Field != field {
		t.Fatalf("error = %v, want %s on %s", err, code, field)
	}
}

// outboxTypes lists the event types of a wallet, in insertion order, with
// the correlation id of each.
func outboxTypes(t *testing.T, walletID string) (types, correlations []string) {
	t.Helper()
	rows, err := env.Owner.Query(t.Context(), `
		SELECT event_type, correlation_id FROM outbox_events
		WHERE message_group_id = $1 ORDER BY occurred_at, event_type`, walletID)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, corr string
		if err := rows.Scan(&typ, &corr); err != nil {
			t.Fatalf("outbox: %v", err)
		}
		types, correlations = append(types, typ), append(correlations, corr)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	return types, correlations
}

// count runs a count(*) query as the owner.
func count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := env.Owner.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// policy is the reference schedule of these tests: a long first delay, so
// that an advanced pending operation is unmistakably earlier.
var policy = func() wagering.ReferenceRetryPolicy {
	p, err := wagering.NewReferenceRetryPolicy(30*time.Second, time.Minute, 3, 10*time.Minute, nil)
	if err != nil {
		panic(err)
	}
	return p
}()

func newProcessWager() *app.ProcessWager {
	return app.NewProcessWager(newUoW(), reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
}

// newProvider isolates the external ids of a test from the parallel ones.
func newProvider() string { return "provider-" + newID() }

// op describes an operation on a wallet; command validates it.
type op struct {
	provider, kind, amount, ext, ref string
	key                              string // "" = provider:ext
	currency                         string // "" = BRL
}

func command(t *testing.T, w wallet.Wallet, o op) wagering.Command {
	t.Helper()
	key, currency := o.key, o.currency
	if key == "" {
		key = o.provider + ":" + o.ext
	}
	if currency == "" {
		currency = "BRL"
	}
	in := wagering.Input{
		IdempotencyKey: ptr(key), ProviderID: ptr(o.provider), ExternalTransactionID: ptr(o.ext),
		PlayerID: ptr(w.PlayerID()), WalletID: ptr(w.ID()), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(o.kind), Money: moneyInput(o.amount, currency),
	}
	if o.ref != "" {
		in.ReferenceExternalTransactionID = ptr(o.ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand %+v: %v", o, err)
	}
	return cmd
}

func request(cmd wagering.Command) app.ProcessRequest {
	return app.ProcessRequest{Command: cmd, Via: wagering.ReceivedViaHTTP, CorrelationID: "corr-" + cmd.ExternalTransactionID()}
}

// process runs the operation and fails the test on error.
func process(t *testing.T, pw *app.ProcessWager, w wallet.Wallet, o op) app.ProcessResult {
	t.Helper()
	res, err := pw.Execute(t.Context(), request(command(t, w, o)))
	if err != nil {
		t.Fatalf("Execute %s %s: %v", o.kind, o.ext, err)
	}
	return res
}

// wantResult fails unless res has the status, failure code and observed balance.
func wantResult(t *testing.T, res app.ProcessResult, status wagering.Status, code wagering.FailureCode, balance string, replay bool) {
	t.Helper()
	tx := res.Tx
	if tx == nil || tx.Status() != status || tx.FailureCode() != code || tx.ResultBalance().String() != balance || res.Replay != replay {
		t.Fatalf("result = %+v, want %s %s balance %q replay %v", describe(res), status, code, balance, replay)
	}
}

func describe(res app.ProcessResult) string {
	if res.Tx == nil {
		return "<nil>"
	}
	return string(res.Tx.Status()) + " " + string(res.Tx.FailureCode()) + " balance " + res.Tx.ResultBalance().String() +
		" replay " + strconv.FormatBool(res.Replay)
}

// wantWallet fails unless the stored wallet has the balance and version.
func wantWallet(t *testing.T, walletID, balance string, version int64) {
	t.Helper()
	w, err := reads().Wallets().Get(t.Context(), walletID)
	if err != nil || w.Balance().String() != balance || w.Version() != version {
		t.Fatalf("wallet = %s v%d, %v; want %s v%d", w.Balance(), w.Version(), err, balance, version)
	}
}

// walletLike is w with another id: a command for a wallet that does not exist.
func walletLike(t *testing.T, w wallet.Wallet, id string) wallet.Wallet {
	t.Helper()
	other, err := wallet.Open(id, w.PlayerID(), w.Balance(), time.Now())
	if err != nil {
		t.Fatalf("wallet.Open: %v", err)
	}
	return other
}
