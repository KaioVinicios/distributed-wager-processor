package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// Codes of lifecycle §5.3 that only the edge produces.
const (
	codeMalformedRequest       = "MALFORMED_REQUEST"
	codeInvalidIdempotencyKey  = "INVALID_IDEMPOTENCY_KEY"
	codeInvalidField           = "INVALID_FIELD"
	codeInvalidAmount          = "INVALID_AMOUNT"
	codeInvalidCurrency        = "INVALID_CURRENCY"
	codeInvalidKind            = "INVALID_KIND"
	codeUnauthenticated        = "UNAUTHENTICATED"
	codeForbidden              = "FORBIDDEN"
	codeProviderMismatch       = "PROVIDER_MISMATCH"
	codeTransactionNotFound    = "TRANSACTION_NOT_FOUND"
	codeRouteNotFound          = "ROUTE_NOT_FOUND"
	codeMethodNotAllowed       = "METHOD_NOT_ALLOWED"
	codeUnsupportedMediaType   = "UNSUPPORTED_MEDIA_TYPE"
	codeInternalError          = "INTERNAL_ERROR"
	codeTemporarilyUnavailable = "TEMPORARILY_UNAVAILABLE"
)

// problemEntry is one line of the catalog: the status, the category and a
// fixed detail that never echoes a value received.
type problemEntry struct {
	status   int
	category wagering.FailureCategory
	detail   string
}

const (
	correctable = wagering.CategoryCorrectable
	definitive  = wagering.CategoryDefinitive
	transient   = wagering.CategoryTransient
)

// problems is the catalog of lifecycle §5.3; api/openapi.yaml lists the same
// codes in the enum of Problem.code.
var problems = map[string]problemEntry{
	"MALFORMED_REQUEST":                {400, correctable, "The request body is not a valid JSON object for this operation."},
	"MISSING_IDEMPOTENCY_KEY":          {400, correctable, "The Idempotency-Key header is required."},
	"INVALID_IDEMPOTENCY_KEY":          {400, correctable, "The Idempotency-Key header must appear once, with 1 to 255 visible ASCII characters."},
	"MISSING_FIELD":                    {400, correctable, "A required field is missing."},
	"INVALID_FIELD":                    {400, correctable, "A field or parameter has an invalid value."},
	"INVALID_AMOUNT":                   {400, correctable, "The amount must be a decimal string with exactly two decimal places."},
	"INVALID_CURRENCY":                 {400, correctable, "The currency is not supported."},
	"INVALID_KIND":                     {400, correctable, "The kind is not supported."},
	"OPENING_NOT_ALLOWED":              {400, correctable, "OPENING is reserved for the internal wallet opening."},
	"ZERO_AMOUNT_NOT_ALLOWED":          {400, correctable, "The amount must be positive for this kind."},
	"LOSS_AMOUNT_MUST_BE_ZERO":         {400, correctable, "A LOSS must have the amount 0.00."},
	"REFERENCE_REQUIRED":               {400, correctable, "REFUND and ROLLBACK require referenceExternalTransactionId."},
	"REFERENCE_NOT_ALLOWED":            {400, correctable, "BET and LOSS do not accept a reference."},
	"SELF_REFERENCE":                   {400, correctable, "An operation cannot reference itself."},
	"UNKNOWN_WALLET":                   {400, correctable, "The wallet does not exist."},
	"UNAUTHENTICATED":                  {401, correctable, "A valid bearer token is required."},
	"FORBIDDEN":                        {403, correctable, "The token does not grant access to this operation."},
	"PROVIDER_MISMATCH":                {403, correctable, "The provider does not match the authenticated identity."},
	"WALLET_NOT_FOUND":                 {404, correctable, "The wallet was not found."},
	"TRANSACTION_NOT_FOUND":            {404, correctable, "The transaction was not found."},
	"ROUTE_NOT_FOUND":                  {404, correctable, "No route matches the path."},
	"METHOD_NOT_ALLOWED":               {405, correctable, "The method is not allowed for this path."},
	"IDEMPOTENCY_KEY_REUSED":           {409, correctable, "The idempotency key was already used with a different payload."},
	"EXTERNAL_TRANSACTION_ID_CONFLICT": {409, correctable, "The external transaction id was already registered with another idempotency key."},
	"WALLET_ALREADY_EXISTS":            {409, definitive, "A wallet already exists for this player and currency."},
	"UNSUPPORTED_MEDIA_TYPE":           {415, correctable, "The request body must be application/json."},
	"INTERNAL_ERROR":                   {500, transient, "An unexpected error occurred; nothing was recorded."},
	"TEMPORARILY_UNAVAILABLE":          {503, transient, "A dependency is temporarily unavailable; retry the same request later."},
}

// problem is the application/problem+json body (RFC 9457).
type problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Code          string `json:"code"`
	Category      string `json:"category"`
	Detail        string `json:"detail"`
	Field         string `json:"field,omitempty"`
	CorrelationID string `json:"correlationId"`
}

// writeProblem answers the code of the catalog; field names the culprit
// field or parameter, when there is one.
func writeProblem(w http.ResponseWriter, r *http.Request, code, field string) {
	e, ok := problems[code]
	if !ok {
		code, e = codeInternalError, problems[codeInternalError]
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.status)
	_ = json.NewEncoder(w).Encode(problem{ // the status line is already sent; nothing useful to do on error
		Type: "about:blank", Title: http.StatusText(e.status), Status: e.status,
		Code: code, Category: string(e.category), Detail: e.detail, Field: field,
		CorrelationID: correlationID(r.Context()),
	})
}
