package sqsconsumer

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Codes and categories of the explicit DLQ sends (lifecycle §5.2–§5.4).
const (
	codeInternalPermanentFailure = "INTERNAL_PERMANENT_FAILURE"
	codeInternalError            = "INTERNAL_ERROR"
	categoryCorrectable          = "CORRECTABLE"
	categoryDefinitive           = "DEFINITIVE"
	categoryTransient            = "TRANSIENT"
)

// actionKind is what happens to a message in the queue.
type actionKind int

const (
	actDelete  actionKind = iota // concluded: DeleteMessage
	actDLQ                       // permanent: SendMessage to the DLQ, then DeleteMessage
	actRetry                     // transient: ChangeMessageVisibility(delay)
	actRelease                   // not processed: ChangeMessageVisibility(0)
)

// action is the decision about one message (spec M5, decision 10), with what
// the metrics count.
type action struct {
	kind           actionKind
	code, category string        // actDLQ
	delay          time.Duration // actRetry
	transient      bool          // the health gate probes the database
	outcome        string        // sqs_messages_processed_total, "" when not concluded by the use case
	duplicate      string        // wager_duplicates_total layer, "" when not a duplicate
}

// conclusion is what the use case answered, reduced to what decides the action.
type conclusion struct {
	duplicate bool
	status    wagering.Status // of the operation; "" on an error
	replay    bool
	err       error
}

func conclude(res app.ConsumeResult, err error) conclusion {
	c := conclusion{duplicate: res.Duplicate, replay: res.Result.Replay, err: err}
	if err == nil && res.Result.Tx != nil {
		c.status = res.Result.Tx.Status()
	}
	return c
}

// decide maps a conclusion to the action of lifecycle §6.2. Only a
// cancellation while the consumer stops releases the message (lifecycle §8);
// every other error that is not input, conflict or permanent is retried with
// backoff, including the unclassified ones (D-05).
func decide(c conclusion, stopping bool, receiveCount int, ceiling time.Duration) action {
	if c.err != nil {
		switch apperrors.Classify(c.err) {
		case apperrors.KindInput, apperrors.KindConflict:
			code := apperrors.CodeOf(c.err)
			if code == "" {
				code = codeMalformedMessage
			}
			return action{kind: actDLQ, code: code, category: categoryCorrectable}
		case apperrors.KindPermanent:
			return action{kind: actDLQ, code: codeInternalError, category: categoryTransient}
		case apperrors.KindTransient, apperrors.KindNotFound, apperrors.KindForbidden, apperrors.KindBusiness:
			// Retried below; the use case returns none of the last three.
		}
		if stopping && errors.Is(c.err, context.Canceled) {
			return action{kind: actRelease}
		}
		return action{kind: actRetry, delay: retryDelay(receiveCount, ceiling), transient: true}
	}
	switch {
	case c.duplicate:
		return action{kind: actDelete, duplicate: "inbox"}
	case c.replay:
		return action{kind: actDelete, outcome: "replay", duplicate: "idempotency"}
	case c.status == wagering.StatusFailed:
		return action{kind: actDLQ, code: codeInternalPermanentFailure, category: categoryDefinitive, outcome: "failed"}
	default:
		return action{kind: actDelete, outcome: strings.ToLower(string(c.status))}
	}
}
