//go:build integration

package postgres_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Covers: OUT-01, OUT-09, OUT-12 (I18: outbox)
func TestOutboxRepository(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	o, err := wagering.OpenWallet(wagering.OpenParams{
		WalletID: newID(), PlayerID: newID(), Initial: brl(t, "25.00"),
		TransactionID: newID(), EntryID: newID(), CorrelationID: "corr-outbox", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	processed, err := events.Seal(newID(), "corr-outbox", "", o.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	changed, err := events.Seal(newID(), "corr-outbox", "cause-1", o.Events[1])
	if err != nil {
		t.Fatal(err)
	}
	if err := newUoW().Do(ctx, func(r app.Repos) error { return r.Outbox().Insert(ctx, processed, changed) }); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	for _, ev := range []events.Envelope{processed, changed} {
		want, err := ev.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var (
			aggType, aggID, group, eventType, correlation string
			causation                                     *string
			version, attempts                             int
			occurred                                      time.Time
			published                                     *time.Time
			samePayload                                   bool
		)
		// JSONB normalizes the text (key order, spaces): the payload is compared as JSON.
		if err := env.Owner.QueryRow(ctx, `SELECT aggregate_type, aggregate_id, message_group_id, event_type,
			event_version, correlation_id, causation_id, occurred_at, attempts, published_at, payload = $2::jsonb
			FROM outbox_events WHERE event_id = $1`, ev.EventID(), string(want)).
			Scan(&aggType, &aggID, &group, &eventType, &version, &correlation, &causation, &occurred, &attempts, &published, &samePayload); err != nil {
			t.Fatalf("read event %s: %v", ev.Type(), err)
		}
		switch {
		case aggType != string(ev.AggregateType()) || aggID != ev.AggregateID() || group != ev.MessageGroupID():
			t.Errorf("%s: aggregate %s/%s group %s", ev.Type(), aggType, aggID, group)
		case eventType != string(ev.Type()) || version != ev.Version() || correlation != "corr-outbox":
			t.Errorf("%s: type %s v%d correlation %s", ev.Type(), eventType, version, correlation)
		case !occurred.Equal(ev.OccurredAt()) || attempts != 0 || published != nil:
			t.Errorf("%s: occurred %v attempts %d published %v", ev.Type(), occurred, attempts, published)
		case !samePayload:
			t.Errorf("%s: stored payload differs from the envelope JSON", ev.Type())
		}
		if (causation == nil) != (ev.CausationID() == "") {
			t.Errorf("%s: causation %v, want %q", ev.Type(), causation, ev.CausationID())
		}
	}

	t.Run("unsealed envelope is rejected before writing", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Outbox().Insert(ctx, events.Envelope{}) })
		wantKind(t, err, apperrors.KindPermanent, events.ErrInvalidEvent)
	})
	t.Run("event recorded twice", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Outbox().Insert(ctx, processed) })
		wantKind(t, err, apperrors.KindPermanent, nil)
	})
}

// Covers: SQS-03, SQS-04 (I18: inbox)
func TestInboxRepository(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	received := time.Now().UTC().Truncate(time.Microsecond)
	m := app.InboxMessage{
		ConsumerName: "wager-transactions", MessageID: "msg-" + newID(), MessageHash: hash,
		MessageType: "WagerTransactionRequested", Outcome: app.InboxRejected,
		ReceivedAt: received, ProcessedAt: received.Add(time.Millisecond),
	}
	if err := newUoW().Do(ctx, func(r app.Repos) error { return r.Inbox().Insert(ctx, m) }); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := reads().Inbox().Find(ctx, m.ConsumerName, m.MessageID)
	if err != nil || got == nil || *got != m {
		t.Fatalf("Find = %+v, %v; want %+v", got, err, m)
	}

	t.Run("unknown message", func(t *testing.T) {
		ctx := t.Context()
		got, err := reads().Inbox().Find(ctx, m.ConsumerName, "msg-unknown")
		if got != nil || err != nil {
			t.Fatalf("Find = %+v, %v; want nil, nil", got, err)
		}
	})
	t.Run("message recorded twice", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Inbox().Insert(ctx, m) })
		wantKind(t, err, apperrors.KindTransient, app.ErrInboxDuplicate)
	})
	t.Run("zero value is rejected", func(t *testing.T) {
		ctx := t.Context()
		err := newUoW().Do(ctx, func(r app.Repos) error { return r.Inbox().Insert(ctx, app.InboxMessage{}) })
		wantKind(t, err, apperrors.KindPermanent, nil)
	})
}
