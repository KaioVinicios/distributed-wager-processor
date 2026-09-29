package httpapi

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// retryAfterSeconds is the Retry-After of every 503 (spec decision 12).
const retryAfterSeconds = "1"

// kindStatus is the status each classified error answers with (spec §6.3).
var kindStatus = map[apperrors.Kind]int{
	apperrors.KindInput:     http.StatusBadRequest,
	apperrors.KindNotFound:  http.StatusNotFound,
	apperrors.KindForbidden: http.StatusForbidden,
	apperrors.KindConflict:  http.StatusConflict,
}

// writeError answers an error of a use case (spec §6.3). A validation error
// keeps its field; KindInput, KindNotFound, KindForbidden and KindConflict
// answer their code; anything transient, including an unclassified error
// (D-05), is 503 with Retry-After; KindPermanent (and a code the catalog does
// not have for that status, a bug) is 500 INTERNAL_ERROR: nothing was recorded.
func writeError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	ctx := r.Context()
	var validation *wagering.ValidationError
	if errors.As(err, &validation) {
		writeProblem(w, r, string(validation.Code), headerField(validation.Field))
		return
	}
	kind, code := apperrors.Classify(err), apperrors.CodeOf(err)
	switch kind {
	case apperrors.KindInput, apperrors.KindNotFound, apperrors.KindForbidden, apperrors.KindConflict:
		if e, ok := problems[code]; ok && e.status == kindStatus[kind] {
			writeProblem(w, r, code, "")
			return
		}
		log.ErrorContext(ctx, "error without a code of its status", "correlationId", correlationID(ctx), "error", err.Error())
		writeProblem(w, r, codeInternalError, "")
	case apperrors.KindTransient:
		log.WarnContext(ctx, "request failed transiently", "correlationId", correlationID(ctx), "error", err.Error())
		w.Header().Set("Retry-After", retryAfterSeconds)
		writeProblem(w, r, codeTemporarilyUnavailable, "")
	case apperrors.KindPermanent, apperrors.KindBusiness:
		log.ErrorContext(ctx, "request failed permanently", "correlationId", correlationID(ctx), "error", err.Error())
		writeProblem(w, r, codeInternalError, "")
	default:
		log.ErrorContext(ctx, "unclassifiable error", "correlationId", correlationID(ctx))
		writeProblem(w, r, codeInternalError, "")
	}
}

// headerField names the idempotency key as the client sent it: a header.
func headerField(field string) string {
	if field == "idempotencyKey" {
		return "Idempotency-Key"
	}
	return field
}

// resultStatus is the status of a recorded operation (D-04): the same for
// the first answer and for every replay.
func resultStatus(s wagering.Status) int {
	switch s {
	case wagering.StatusProcessed:
		return http.StatusOK
	case wagering.StatusPendingReference:
		return http.StatusAccepted
	case wagering.StatusRejected:
		return http.StatusUnprocessableEntity
	case wagering.StatusFailed, wagering.StatusPending: // PENDING is never persisted (D-05)
		return http.StatusInternalServerError
	}
	return http.StatusInternalServerError
}
