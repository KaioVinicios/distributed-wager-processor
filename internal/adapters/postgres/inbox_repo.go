package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/KaioVinicios/pda/internal/app"
)

type inboxRepo struct{ q querier }

const inboxColumns = `consumer_name, message_id, message_hash, message_type, transaction_id, outcome, received_at, processed_at`

func (r inboxRepo) Find(ctx context.Context, consumer, messageID string) (*app.InboxMessage, error) {
	var m app.InboxMessage
	var txID *string
	err := r.q.QueryRow(ctx, `SELECT `+inboxColumns+` FROM inbox_messages
		WHERE consumer_name = $1 AND message_id = $2`, consumer, messageID).
		Scan(&m.ConsumerName, &m.MessageID, &m.MessageHash, &m.MessageType, &txID, &m.Outcome, &m.ReceivedAt, &m.ProcessedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, translate(err)
	}
	m.TransactionID = text(txID)
	m.ReceivedAt, m.ProcessedAt = m.ReceivedAt.UTC(), m.ProcessedAt.UTC()
	return &m, nil
}

// Insert records the message in the transaction of its domain changes
// (SQS-04). The table CHECKs refuse a zero value (empty hash, unknown outcome).
func (r inboxRepo) Insert(ctx context.Context, m app.InboxMessage) error {
	_, err := r.q.Exec(ctx, `INSERT INTO inbox_messages (`+inboxColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		m.ConsumerName, m.MessageID, m.MessageHash, m.MessageType, nullableText(m.TransactionID),
		string(m.Outcome), m.ReceivedAt, m.ProcessedAt)
	return translate(err)
}
