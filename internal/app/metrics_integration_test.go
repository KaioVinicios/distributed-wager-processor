//go:build integration

package app_test

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// recordingMetrics keeps every report, in order.
type recordingMetrics struct {
	app.NopMetrics
	mu         sync.Mutex
	concluded  []string // channel/kind/outcome/failureCode
	duplicates []string // channel/layer
	conflicts  []string
}

func (m *recordingMetrics) WagerConcluded(channel, kind, outcome, code string, _ time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.concluded = append(m.concluded, channel+"/"+kind+"/"+outcome+"/"+code)
}

func (m *recordingMetrics) WagerDuplicate(channel, layer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.duplicates = append(m.duplicates, channel+"/"+layer)
}

func (m *recordingMetrics) Conflict(reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.conflicts = append(m.conflicts, reason)
}

func (m *recordingMetrics) snapshot() (concluded, duplicates, conflicts []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.concluded...), append([]string(nil), m.duplicates...), append([]string(nil), m.conflicts...)
}

func wantList(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

// Covers: OBS-03 (U23)
// Sensitivity: counting a replay as a new conclusion → "concluded" gets a second BET entry; counting HTTP duplicates in the SQS path → "duplicates" gets an sqs entry.
func TestProcessWagerMetrics(t *testing.T) {
	t.Parallel()

	t.Run("a new operation is counted once and its replay is a duplicate", func(t *testing.T) {
		t.Parallel()
		m := &recordingMetrics{}
		pw := newProcessWager().WithMetrics(m)
		w, p := openWallet(t, "100.00"), newProvider()

		bet := op{provider: p, kind: "BET", amount: "30.00", ext: "bet-1"}
		process(t, pw, w, bet)
		process(t, pw, w, bet) // same key and content: replay
		process(t, pw, w, op{provider: p, kind: "BET", amount: "500.00", ext: "bet-2"})

		concluded, duplicates, conflicts := m.snapshot()
		wantList(t, "concluded", concluded, "http/BET/processed/", "http/BET/rejected/INSUFFICIENT_FUNDS")
		wantList(t, "duplicates", duplicates, "http/idempotency")
		wantList(t, "conflicts", conflicts)
	})

	t.Run("a replay over SQS is left to the consumer", func(t *testing.T) {
		t.Parallel()
		m := &recordingMetrics{}
		pw := newProcessWager().WithMetrics(m)
		w, p := openWallet(t, "100.00"), newProvider()
		req := request(command(t, w, op{provider: p, kind: "BET", amount: "10.00", ext: "bet-1"}))
		req.Via = wagering.ReceivedViaSQS
		for range 2 {
			if _, err := pw.Execute(t.Context(), req); err != nil {
				t.Fatalf("Execute: %v", err)
			}
		}
		concluded, duplicates, _ := m.snapshot()
		wantList(t, "concluded", concluded, "sqs/BET/processed/")
		wantList(t, "duplicates", duplicates)
	})

	t.Run("a pending reference is counted as pending_reference", func(t *testing.T) {
		t.Parallel()
		m := &recordingMetrics{}
		pw := newProcessWager().WithMetrics(m)
		w, p := openWallet(t, "100.00"), newProvider()
		process(t, pw, w, op{provider: p, kind: "REFUND", amount: "10.00", ext: "refund-1", ref: "bet-missing"})
		concluded, _, _ := m.snapshot()
		wantList(t, "concluded", concluded, "http/REFUND/pending_reference/")
	})
}

// Covers: OBS-01, OBS-02 (U23; spec M7, decision 12)
// Sensitivity: logging the amount → the "no amount" check fails; dropping messageId for SQS → the SQS line lacks it.
func TestWagerConcludedLog(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	pw := app.NewProcessWager(newUoW(), reads(), app.SystemClock{}, app.UUIDv7{}, policy, slog.New(slog.NewJSONHandler(&logs, nil)))
	w, p := openWallet(t, "1000.00"), newProvider()

	req := request(command(t, w, op{provider: p, kind: "BET", amount: "137.29", ext: "bet-1", key: "secret-key-7"}))
	req.Via = wagering.ReceivedViaSQS
	req.Inbox = &app.InboxReceipt{Consumer: app.ConsumerName, MessageID: "msg-777", MessageHash: strings.Repeat("a", 64), MessageType: "wager.requested", ReceivedAt: time.Now()}
	res, err := pw.Execute(t.Context(), req)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	line := logs.String()
	for _, want := range []string{
		`"msg":"wager concluded"`, `"transactionId":"` + res.Tx.ID() + `"`, `"walletId":"` + w.ID() + `"`,
		`"providerId":"` + p + `"`, `"correlationId":"corr-bet-1"`, `"messageId":"msg-777"`,
		`"channel":"sqs"`, `"kind":"BET"`, `"outcome":"processed"`, `"replay":false`,
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("log %s lacks %s", line, want)
		}
	}
	for _, secret := range []string{"137.29", "secret-key-7", "862.71"} {
		if strings.Contains(line, secret) {
			t.Fatalf("log %s leaks %q", line, secret)
		}
	}
}
