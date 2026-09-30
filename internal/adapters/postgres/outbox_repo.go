package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/ident"
)

type outboxRepo struct{ q querier }

// Insert records sealed envelopes as immutable snapshots (OUT-01, OUT-09). The
// payload is the envelope JSON; next_attempt_at uses the database clock, the
// same one the publisher's claim compares with.
func (r outboxRepo) Insert(ctx context.Context, envs ...events.Envelope) error {
	for _, env := range envs {
		payload, err := env.MarshalJSON()
		if err != nil {
			return invalidValue(err)
		}
		_, err = r.q.Exec(ctx, `INSERT INTO outbox_events
			(event_id, aggregate_type, aggregate_id, message_group_id, event_type, event_version,
			 payload, correlation_id, causation_id, occurred_at, next_attempt_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())`,
			env.EventID(), string(env.AggregateType()), env.AggregateID(), env.MessageGroupID(),
			string(env.Type()), env.Version(), json.RawMessage(payload), env.CorrelationID(),
			nullableText(env.CausationID()), env.OccurredAt())
		if err != nil {
			return translate(err)
		}
	}
	return nil
}

// OutboxStore implements app.OutboxStore over the pool (D-13): each method
// is one statement, so the claim is the short transaction of the publisher.
type OutboxStore struct{ pool *pgxpool.Pool }

var _ app.OutboxStore = (*OutboxStore)(nil)

// NewOutboxStore is the store the outbox publisher uses.
func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore { return &OutboxStore{pool: pool} }

// Claim is the claim of data-model §6. A previous owner means the lease had
// expired: abandoned work taken over.
func (s *OutboxStore) Claim(ctx context.Context, owner string, lease time.Duration, limit int) ([]app.PendingEvent, error) {
	rows, err := s.pool.Query(ctx, `WITH due AS (
			SELECT event_id, locked_by AS previous_owner FROM outbox_events
			WHERE published_at IS NULL
			  AND next_attempt_at <= now()
			  AND (locked_until IS NULL OR locked_until < now())
			ORDER BY next_attempt_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE outbox_events o
		SET locked_by = $2, locked_until = now() + $3::interval
		FROM due WHERE o.event_id = due.event_id
		RETURNING o.event_id::text, o.message_group_id, o.event_type, o.event_version,
		          o.correlation_id, o.payload, o.occurred_at, o.attempts, due.previous_owner`,
		limit, owner, lease)
	if err != nil {
		return nil, translate(err)
	}
	defer rows.Close()
	var out []app.PendingEvent
	for rows.Next() {
		var e app.PendingEvent
		var previous *string
		if err := rows.Scan(&e.EventID, &e.MessageGroupID, &e.EventType, &e.EventVersion,
			&e.CorrelationID, &e.Payload, &e.OccurredAt, &e.Attempts, &previous); err != nil {
			return nil, translate(err)
		}
		e.Reclaimed = previous != nil
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, translate(err)
	}
	return out, nil
}

// MarkPublished is the conditional confirmation of data-model §6.
func (s *OutboxStore) MarkPublished(ctx context.Context, eventID, owner string) (time.Time, bool, error) {
	if !ident.Valid(eventID) { // not a canonical UUID: no such lease, and no 22P02 from the database
		return time.Time{}, false, nil
	}
	var at time.Time
	err := s.pool.QueryRow(ctx, `UPDATE outbox_events
		SET published_at = now(), locked_by = NULL, locked_until = NULL
		WHERE event_id = $1 AND locked_by = $2 AND published_at IS NULL
		RETURNING published_at`, eventID, owner).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, translate(err)
	}
	return at, true, nil
}

// MarkFailed is the failure of data-model §6: the attempt is counted, the
// next one scheduled and the lease released.
func (s *OutboxStore) MarkFailed(ctx context.Context, eventID, owner string, retryIn time.Duration, reason string) (bool, error) {
	if !ident.Valid(eventID) {
		return false, nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE outbox_events
		SET attempts = attempts + 1, next_attempt_at = now() + $3::interval,
		    locked_by = NULL, locked_until = NULL, last_error = $4
		WHERE event_id = $1 AND locked_by = $2 AND published_at IS NULL`, eventID, owner, retryIn, reason)
	if err != nil {
		return false, translate(err)
	}
	return tag.RowsAffected() == 1, nil
}

// Backlog is the backlog query of data-model §6.
func (s *OutboxStore) Backlog(ctx context.Context) (app.OutboxBacklog, error) {
	var b app.OutboxBacklog
	var ageMicros int64
	err := s.pool.QueryRow(ctx, `SELECT count(*),
		COALESCE(GREATEST(EXTRACT(EPOCH FROM now() - min(occurred_at)), 0) * 1000000, 0)::bigint
		FROM outbox_events WHERE published_at IS NULL`).Scan(&b.Pending, &ageMicros)
	if err != nil {
		return app.OutboxBacklog{}, translate(err)
	}
	b.OldestAge = time.Duration(ageMicros) * time.Microsecond
	return b, nil
}
