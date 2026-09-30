//go:build integration

package references_test

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/adapters/postgres"
	"github.com/KaioVinicios/pda/internal/adapters/references"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/internal/observability"
	"github.com/KaioVinicios/pda/test/testkit"
)

func newID() string { return uuid.Must(uuid.NewV7()).String() }

func ptr(s string) *string { return &s }

// operation validates an operation of provider on w; ref "" = no reference.
func operation(t *testing.T, w wallet.Wallet, provider, kind, amount, ext, ref string) wagering.Command {
	t.Helper()
	in := wagering.Input{
		IdempotencyKey: ptr(provider + ":" + ext), ProviderID: ptr(provider), ExternalTransactionID: ptr(ext),
		PlayerID: ptr(w.PlayerID()), WalletID: ptr(w.ID()), RoundID: ptr("round-1"), GameID: ptr("game-1"),
		Kind: ptr(kind), Money: &wagering.MoneyInput{Amount: ptr(amount), Currency: ptr("BRL")},
	}
	if ref != "" {
		in.ReferenceExternalTransactionID = ptr(ref)
	}
	cmd, err := wagering.NewCommand(in)
	if err != nil {
		t.Fatalf("NewCommand %s %s: %v", kind, ext, err)
	}
	return cmd
}

// Covers: E7, TX-09, OPS-12 (spec M6, decision 3)
// Smoke test with two real workers: removing the horizon check in lockPending was NOT detected here (3 runs, the
// window is narrow); the deterministic proofs of decision 3 are TestResolveReferencesSkips and TestResolveReferencesConcurrent.
//
// Two workers, each with its own pool, run over the same database while the
// BETs of 40 waiting REFUNDs arrive from both. Every REFUND is processed once,
// and every wallet is consistent.
func TestConcurrentWorkers(t *testing.T) {
	e := testkit.NewTestEnv(t, "refs_concurrent")
	second, err := pgxpool.New(t.Context(), e.DB.AppURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	// Long limits: nothing expires while the test runs.
	policy, err := wagering.NewReferenceRetryPolicy(100*time.Millisecond, time.Second, 50, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	build := func(pool *pgxpool.Pool) (*app.ProcessWager, *references.Worker) {
		pw := app.NewProcessWager(postgres.NewUnitOfWork(pool, e.Config()), postgres.NewRepos(pool), app.SystemClock{}, app.UUIDv7{}, policy, log)
		worker := references.NewWorker(app.NewResolveReferences(pw), observability.NewMetrics(observability.NewRegistry()), log,
			references.Options{BatchSize: 5, PollInterval: 20 * time.Millisecond})
		return pw, worker
	}
	pw1, worker1 := build(e.App)
	pw2, worker2 := build(second)

	const wallets, perWallet = 10, 4
	provider := "provider-" + newID()
	open := app.NewOpenWallet(postgres.NewUnitOfWork(e.App, e.Config()), app.SystemClock{}, app.UUIDv7{})
	ws := make([]wallet.Wallet, wallets)
	for i := range ws {
		amount := "100.00"
		if ws[i], err = open.Execute(t.Context(), app.OpenWalletInput{
			PlayerID: ptr(newID()), InitialBalance: &wagering.MoneyInput{Amount: &amount, Currency: ptr("BRL")},
		}, "corr-open"); err != nil {
			t.Fatalf("OpenWallet: %v", err)
		}
	}
	name := func(prefix string, i, k int) string { return prefix + "-" + strconv.Itoa(i) + "-" + strconv.Itoa(k) }
	for i, w := range ws { // the REFUNDs come first and wait
		for k := range perWallet {
			res, err := pw1.Execute(t.Context(), app.ProcessRequest{
				Command: operation(t, w, provider, "REFUND", "10.00", name("refund", i, k), name("bet", i, k)),
				Via:     wagering.ReceivedViaHTTP, CorrelationID: "corr-" + name("refund", i, k),
			})
			if err != nil || res.Tx.Status() != wagering.StatusPendingReference {
				t.Fatalf("REFUND %d-%d: %v, %v; want PENDING_REFERENCE", i, k, res.Tx, err)
			}
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	var running sync.WaitGroup
	for _, worker := range []*references.Worker{worker1, worker2} {
		running.Go(func() { worker.Run(ctx) })
	}
	t.Cleanup(func() { cancel(); running.Wait() })

	var wg sync.WaitGroup // the BETs arrive from both instances and wake their REFUNDs
	for i, w := range ws {
		for k := range perWallet {
			cmd := operation(t, w, provider, "BET", "10.00", name("bet", i, k), "")
			wg.Go(func() {
				pw := pw1
				if (i+k)%2 == 1 {
					pw = pw2
				}
				res, err := pw.Execute(ctx, app.ProcessRequest{
					Command: cmd, Via: wagering.ReceivedViaHTTP, CorrelationID: "corr-" + name("bet", i, k),
				})
				if err != nil || res.Tx.Status() != wagering.StatusProcessed {
					t.Errorf("BET %d-%d: %v, %v", i, k, res.Tx, err)
				}
			})
		}
	}
	wg.Wait()

	testkit.Eventually(t, 20*time.Second, "every REFUND processed", func(ctx context.Context) (bool, error) {
		var n int
		err := e.Owner.QueryRow(ctx, `SELECT count(*) FROM wager_transactions
			WHERE provider_id = $1 AND kind = 'REFUND' AND status = 'PROCESSED'`, provider).Scan(&n)
		return n == wallets*perWallet, err
	})
	var rejected int
	if err := e.Owner.QueryRow(t.Context(), `SELECT count(*) FROM wager_transactions
		WHERE provider_id = $1 AND status IN ('REJECTED','FAILED')`, provider).Scan(&rejected); err != nil || rejected != 0 {
		t.Fatalf("%d operations rejected or failed, %v; want none", rejected, err)
	}
	for _, w := range ws {
		var balance string
		var entries int
		if err := e.Owner.QueryRow(t.Context(), `SELECT (balance_minor / 100.0)::numeric(12,2)::text,
			(SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1) FROM wallets WHERE id = $1`, w.ID()).Scan(&balance, &entries); err != nil {
			t.Fatal(err)
		}
		if balance != "100.00" || entries != 1+2*perWallet { // the opening, the BETs and the REFUNDs
			t.Fatalf("wallet %s: balance %s, %d entries; want 100.00 and %d", w.ID(), balance, entries, 1+2*perWallet)
		}
		for _, check := range []func(context.Context, *pgxpool.Pool, string) ([]string, error){testkit.LedgerProblems, testkit.OutboxProblems} {
			problems, err := check(t.Context(), e.Owner, w.ID())
			if err != nil || len(problems) != 0 {
				t.Errorf("wallet %s: %v, %v", w.ID(), problems, err)
			}
		}
	}
}
