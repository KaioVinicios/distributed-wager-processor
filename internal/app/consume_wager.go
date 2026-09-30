package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// ConsumerName identifies the wager consumer in inbox_messages (D-12).
const ConsumerName = "wager-transactions-consumer"

// CodeMessageHashMismatch: the same messageId was concluded with other
// content (lifecycle §5.4).
const CodeMessageHashMismatch = "MESSAGE_HASH_MISMATCH"

// WagerMessage is a parsed WagerTransactionRequested (messaging.md §3).
type WagerMessage struct {
	MessageID     string
	MessageHash   string // SHA-256 (lowercase hex) of the canonical {type, data}
	MessageType   string
	CorrelationID string
	Input         wagering.Input
	ReceivedAt    time.Time
}

// ConsumeResult is how a message was concluded. Duplicate: the inbox already
// had it with the same hash, and nothing was done.
type ConsumeResult struct {
	Duplicate bool
	Result    ProcessResult
}

// ConsumeWager is the use case of the SQS consumer (lifecycle §6.2).
type ConsumeWager struct {
	reads  Repos
	wagers *ProcessWager
}

// NewConsumeWager builds the use case. reads are the repositories over the
// pool, used for the inbox lookup before the transaction.
func NewConsumeWager(reads Repos, wagers *ProcessWager) *ConsumeWager {
	return &ConsumeWager{reads: reads, wagers: wagers}
}

// Execute concludes one message: the inbox first (a redelivery is a duplicate,
// the same messageId with other content is MESSAGE_HASH_MISMATCH), then the
// stateless validation, then ProcessWager with the inbox receipt, which
// records it with the outcome. Errors are those of ProcessWager plus
// KindInput for the hash and the validation; nothing is recorded with them.
//
// Two consumers with the same message race on the inbox primary key: the
// loser rolls back and starts again from the inbox, which then has the row
// (spec M5, decision 3); after maxAttempts the race is returned, transient.
func (c *ConsumeWager) Execute(ctx context.Context, m WagerMessage) (ConsumeResult, error) {
	var err error
	for range maxAttempts {
		var res ConsumeResult
		if res, err = c.attempt(ctx, m); !errors.Is(err, ErrInboxDuplicate) {
			return res, err
		}
	}
	return ConsumeResult{}, err
}

func (c *ConsumeWager) attempt(ctx context.Context, m WagerMessage) (ConsumeResult, error) {
	seen, err := c.reads.Inbox().Find(ctx, ConsumerName, m.MessageID)
	if err != nil {
		return ConsumeResult{}, err
	}
	if seen != nil {
		if seen.MessageHash != m.MessageHash {
			return ConsumeResult{}, apperrors.New(apperrors.KindInput, CodeMessageHashMismatch,
				fmt.Errorf("app: message %s already concluded with other content", m.MessageID))
		}
		return ConsumeResult{Duplicate: true}, nil
	}
	cmd, err := wagering.NewCommand(m.Input)
	if err != nil {
		return ConsumeResult{}, domainError(err)
	}
	res, err := c.wagers.Execute(ctx, ProcessRequest{
		Command: cmd, Via: wagering.ReceivedViaSQS, CorrelationID: m.CorrelationID, CausationID: m.MessageID,
		Inbox: &InboxReceipt{
			Consumer: ConsumerName, MessageID: m.MessageID, MessageHash: m.MessageHash,
			MessageType: m.MessageType, ReceivedAt: m.ReceivedAt,
		},
	})
	return ConsumeResult{Result: res}, err
}
