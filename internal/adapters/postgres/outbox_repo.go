package postgres

import (
	"context"
	"encoding/json"

	"github.com/KaioVinicios/pda/internal/domain/events"
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
