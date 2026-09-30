//go:build integration

package app_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

func newConsumeWager() *app.ConsumeWager { return app.NewConsumeWager(reads(), newProcessWager()) }

// message is o on w as a WagerTransactionRequested with messageID. The hash
// stands for the adapter's: any 64 hex digits that change with the content.
func message(w wallet.Wallet, o op, messageID string) app.WagerMessage {
	in := input(w, o)
	sum := sha256.Sum256([]byte(o.provider + o.kind + o.amount + o.ext + o.ref + o.key))
	return app.WagerMessage{
		MessageID: messageID, MessageHash: hex.EncodeToString(sum[:]), MessageType: "WagerTransactionRequested",
		CorrelationID: "corr-" + messageID, Input: in, ReceivedAt: time.Now(),
	}
}

// consume runs the message and fails the test on error.
func consume(t *testing.T, c *app.ConsumeWager, m app.WagerMessage) app.ConsumeResult {
	t.Helper()
	res, err := c.Execute(t.Context(), m)
	if err != nil {
		t.Fatalf("Execute %s: %v", m.MessageID, err)
	}
	return res
}

// inboxRow is the stored inbox row of messageID ("" outcome when absent).
type inboxRow struct{ hash, typ, txID, outcome string }

func inbox(t *testing.T, messageID string) inboxRow {
	t.Helper()
	var r inboxRow
	var txID *string
	err := env.Owner.QueryRow(t.Context(), `SELECT message_hash, message_type, transaction_id::text, outcome
		FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`, app.ConsumerName, messageID).
		Scan(&r.hash, &r.typ, &txID, &r.outcome)
	if err != nil && err.Error() != "no rows in result set" {
		t.Fatalf("inbox %s: %v", messageID, err)
	}
	if txID != nil {
		r.txID = *txID
	}
	return r
}

// Covers: SQS-02, SQS-03, SQS-04, SQS-06, SQS-08 (spec M5 §3)
func TestConsumeWager(t *testing.T) {
	t.Parallel()

	t.Run("new outcomes record the inbox in their transaction", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		c := newConsumeWager()
		cases := []struct {
			o      op
			status wagering.Status
		}{
			{op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, wagering.StatusProcessed},
			{op{provider: p, kind: "BET", amount: "500.00", ext: "bet-2"}, wagering.StatusRejected},
			{op{provider: p, kind: "REFUND", amount: "5.00", ext: "refund-1", ref: "bet-9"}, wagering.StatusPendingReference},
		}
		for _, tc := range cases {
			m := message(w, tc.o, "msg-"+p+"-"+tc.o.ext)
			res := consume(t, c, m)
			if res.Duplicate || res.Result.Replay || res.Result.Tx.Status() != tc.status {
				t.Fatalf("%s: result = %+v %s, want a new %s", tc.o.ext, res.Duplicate, describe(res.Result), tc.status)
			}
			tx := res.Result.Tx
			if got := inbox(t, m.MessageID); got != (inboxRow{m.MessageHash, "WagerTransactionRequested", tx.ID(), string(tc.status)}) {
				t.Fatalf("%s: inbox = %+v", tc.o.ext, got)
			}
			if tx.ReceivedVia() != wagering.ReceivedViaSQS || tx.CorrelationID() != m.CorrelationID {
				t.Fatalf("%s: via %s correlation %s", tc.o.ext, tx.ReceivedVia(), tx.CorrelationID())
			}
			if n := count(t, `SELECT count(*) FROM outbox_events WHERE causation_id = $1`, m.MessageID); n == 0 {
				t.Fatalf("%s: no event caused by the message", tc.o.ext)
			}
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})

	t.Run("a redelivery with the same hash is a duplicate", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		c := newConsumeWager()
		m := message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p)
		first := consume(t, c, m)
		again := consume(t, c, m)
		if !again.Duplicate || again.Result.Tx != nil {
			t.Fatalf("redelivery = %+v, want a duplicate", again)
		}
		if got := inbox(t, m.MessageID); got.outcome != "PROCESSED" || got.txID != first.Result.Tx.ID() {
			t.Fatalf("inbox = %+v", got)
		}
		if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 1 {
			t.Fatalf("%d operations, want 1", n)
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})

	t.Run("the same messageId with other content is MESSAGE_HASH_MISMATCH", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		c := newConsumeWager()
		consume(t, c, message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p))
		_, err := c.Execute(t.Context(), message(w, op{provider: p, kind: "BET", amount: "31.00", ext: "bet-1"}, "msg-"+p))
		wantError(t, err, apperrors.KindInput, app.CodeMessageHashMismatch)
		wantWallet(t, w.ID(), "70.00", 2)
	})

	t.Run("invalid input and conflicts record nothing", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		c := newConsumeWager()
		consume(t, c, message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p+"-1"))
		cases := []struct {
			name string
			m    app.WagerMessage
			kind apperrors.Kind
			code string
		}{
			{
				"opening", message(w, op{provider: p, kind: "OPENING", amount: "1.00", ext: "open-1"}, "msg-"+p+"-2"),
				apperrors.KindInput, string(wagering.InputOpeningNotAllowed),
			},
			{
				"unknown wallet", message(walletLike(t, w, newID()), op{provider: p, kind: "BET", amount: "1.00", ext: "bet-2"}, "msg-"+p+"-3"),
				apperrors.KindInput, app.CodeUnknownWallet,
			},
			{
				"key reused", message(w, op{provider: p, kind: "BET", amount: "31.00", ext: "bet-1"}, "msg-"+p+"-4"),
				apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED",
			},
		}
		for _, tc := range cases {
			_, err := c.Execute(t.Context(), tc.m)
			wantError(t, err, tc.kind, tc.code)
			if got := inbox(t, tc.m.MessageID); got.outcome != "" {
				t.Fatalf("%s: inbox = %+v, want no row", tc.name, got)
			}
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})
}

// Covers: SQS-02, IDEM-04 (C10a in process)
func TestConsumeWagerReplayAcrossChannels(t *testing.T) {
	t.Parallel()
	w, p := openWallet(t, "100.00"), newProvider()
	bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}
	overHTTP := process(t, newProcessWager(), w, bet)

	m := message(w, bet, "msg-"+p)
	res := consume(t, newConsumeWager(), m)
	if res.Duplicate || !res.Result.Replay || res.Result.Tx.ID() != overHTTP.Tx.ID() {
		t.Fatalf("result = %+v %s, want the replay of %s", res.Duplicate, describe(res.Result), overHTTP.Tx.ID())
	}
	if got := inbox(t, m.MessageID); got.outcome != "IDEMPOTENT_REPLAY" || got.txID != overHTTP.Tx.ID() {
		t.Fatalf("inbox = %+v", got)
	}
	if n := count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1`, w.ID()); n != 2 {
		t.Fatalf("%d entries, want the opening and one debit", n)
	}
}

// Covers: TX-06, SQS-07, TST-I03 (I03b, the SQS part)
func TestConsumeWagerPermanentFailure(t *testing.T) {
	t.Parallel()
	w, p := openWallet(t, "100.00"), newProvider()
	forced := apperrors.New(apperrors.KindPermanent, "", errors.New("forced permanent failure"))
	uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
		return faultyRepos{Repos: r, outbox: failingOutbox{r.Outbox(), forced}}
	}}
	pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))
	m := message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p)

	res := consume(t, app.NewConsumeWager(reads(), pw), m)
	wantResult(t, res.Result, wagering.StatusFailed, wagering.FailureInternalPermanentFailure, "", false)
	if got := inbox(t, m.MessageID); got.outcome != "FAILED" || got.txID != res.Result.Tx.ID() {
		t.Fatalf("inbox = %+v, want FAILED with the operation", got)
	}
	if n := count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1`, res.Result.Tx.ID()); n != 0 {
		t.Fatalf("%d entries for a FAILED operation", n)
	}
	wantWallet(t, w.ID(), "100.00", 1)
}

// Covers: SQS-04
// Sensitivity: the inbox of a new outcome written over the pool after the
// commit → the operation is recorded and "error = <nil>".
func TestConsumeWagerAtomicInbox(t *testing.T) {
	t.Parallel()
	w, p := openWallet(t, "100.00"), newProvider()
	broken := errors.New("inbox unavailable")
	uow := &faultyUoW{UnitOfWork: newUoW(), wrap: func(r app.Repos, _ int) app.Repos {
		return faultyRepos{Repos: r, inbox: failingInbox{r.Inbox(), broken}}
	}}
	pw := app.NewProcessWager(uow, reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.DiscardHandler))

	_, err := app.NewConsumeWager(reads(), pw).Execute(t.Context(), message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p))
	if !errors.Is(err, broken) || apperrors.Classify(err) != apperrors.KindTransient {
		t.Fatalf("error = %v, want the transient inbox failure", err)
	}
	if n := count(t, `SELECT count(*) FROM wager_transactions WHERE provider_id = $1`, p); n != 0 {
		t.Fatalf("%d operations recorded without their inbox row", n)
	}
	wantWallet(t, w.ID(), "100.00", 1)
}

// Covers: SQS-03, CONC-03
// Sensitivity: ConsumeWager without the new round on ErrInboxDuplicate →
// "delivery N: TRANSIENT: app: message already recorded in the inbox".
func TestConsumeWagerInboxRace(t *testing.T) {
	t.Parallel()

	t.Run("the same message 20 times at once", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		m := message(w, op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}, "msg-"+p)
		results, errs := consumeConcurrently(t, 20, func(int) app.WagerMessage { return m })
		fresh := 0
		for i, res := range results {
			if errs[i] != nil {
				t.Fatalf("delivery %d: %v", i, errs[i])
			}
			if !res.Duplicate {
				fresh++
			}
		}
		if fresh != 1 {
			t.Fatalf("%d deliveries concluded the message, want 1 and 19 duplicates", fresh)
		}
		if n := count(t, `SELECT count(*) FROM inbox_messages WHERE message_id = $1`, m.MessageID); n != 1 {
			t.Fatalf("%d inbox rows", n)
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})

	t.Run("10 messages of the same operation at once", func(t *testing.T) {
		t.Parallel()
		w, p := openWallet(t, "100.00"), newProvider()
		bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}
		_, errs := consumeConcurrently(t, 10, func(i int) app.WagerMessage {
			return message(w, bet, "msg-"+p+"-"+string(rune('a'+i)))
		})
		for i, err := range errs {
			if err != nil {
				t.Fatalf("message %d: %v", i, err)
			}
		}
		if n := count(t, `SELECT count(*) FROM inbox_messages WHERE message_id LIKE $1 AND outcome = 'PROCESSED'`, "msg-"+p+"-%"); n != 1 {
			t.Fatalf("%d PROCESSED inbox rows, want 1", n)
		}
		if n := count(t, `SELECT count(*) FROM inbox_messages WHERE message_id LIKE $1 AND outcome = 'IDEMPOTENT_REPLAY'`, "msg-"+p+"-%"); n != 9 {
			t.Fatalf("%d IDEMPOTENT_REPLAY inbox rows, want 9", n)
		}
		wantWallet(t, w.ID(), "70.00", 2)
	})
}

// consumeConcurrently runs n deliveries at once, each with its own use case.
func consumeConcurrently(t *testing.T, n int, msg func(i int) app.WagerMessage) ([]app.ConsumeResult, []error) {
	t.Helper()
	results, errs := make([]app.ConsumeResult, n), make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		c, m := newConsumeWager(), msg(i)
		wg.Go(func() {
			<-start
			results[i], errs[i] = c.Execute(t.Context(), m)
		})
	}
	close(start)
	wg.Wait()
	return results, errs
}
