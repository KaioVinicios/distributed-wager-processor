package wagering

import (
	"fmt"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/ident"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// WagerTransaction is an operation on a wallet (CHALLENGE §6.3). It is born
// PENDING, in memory only, and changes state only through the transitions of
// transitions.go.
type WagerTransaction struct {
	id                             string
	origin                         Origin
	kind                           Kind
	status                         Status
	walletID                       string
	playerID                       string
	money                          money.Money
	providerID                     string
	externalTransactionID          string
	idempotencyKey                 string
	payloadHash                    string
	roundID                        string
	gameID                         string
	referenceExternalTransactionID string
	receivedVia                    ReceivedVia
	referenceTransactionID         string
	failureCode                    FailureCode
	resultBalance                  money.Money
	attempts                       int
	nextAttemptAt                  time.Time
	expiresAt                      time.Time
	correlationID                  string
	createdAt                      time.Time
	updatedAt                      time.Time
	completedAt                    time.Time
}

// Snapshot is the persisted state (data-model §3.2). Absent values are zero
// values: "" for strings, money.Money{} for ResultBalance, the zero time.Time.
type Snapshot struct {
	ID                             string
	Origin                         Origin
	Kind                           Kind
	Status                         Status
	WalletID                       string
	PlayerID                       string
	Money                          money.Money
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
	ReceivedVia                    ReceivedVia
	ReferenceTransactionID         string
	FailureCode                    FailureCode
	ResultBalance                  money.Money
	Attempts                       int
	NextAttemptAt                  time.Time
	ExpiresAt                      time.Time
	CorrelationID                  string
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
	CompletedAt                    time.Time
}

// NewExternal creates a PENDING provider operation from a validated command.
func NewExternal(id string, cmd Command, via ReceivedVia, correlationID string, now time.Time) (*WagerTransaction, error) {
	switch {
	case cmd.payloadHash == "":
		return nil, fmt.Errorf("%w: command", ErrUninitialized)
	case !ident.Valid(id):
		return nil, invalidArg("id must be a canonical UUID")
	case !via.Valid():
		return nil, invalidArg("received via must be HTTP or SQS")
	case !validText(correlationID):
		return nil, invalidArg("correlation id must have 1 to 128 characters")
	case now.IsZero():
		return nil, invalidArg("now is required")
	}
	now = normalize(now)
	return &WagerTransaction{
		id:                             id,
		origin:                         OriginExternal,
		kind:                           cmd.kind,
		status:                         StatusPending,
		walletID:                       cmd.walletID,
		playerID:                       cmd.playerID,
		money:                          cmd.money,
		providerID:                     cmd.providerID,
		externalTransactionID:          cmd.externalTransactionID,
		idempotencyKey:                 cmd.idempotencyKey,
		payloadHash:                    cmd.payloadHash,
		roundID:                        cmd.roundID,
		gameID:                         cmd.gameID,
		referenceExternalTransactionID: cmd.referenceExternalTransactionID,
		receivedVia:                    via,
		correlationID:                  correlationID,
		createdAt:                      now,
		updatedAt:                      now,
	}, nil
}

// NewOpening creates the PENDING internal OPENING of a positive initial balance.
func NewOpening(id, walletID, playerID string, amount money.Money, correlationID string, now time.Time) (*WagerTransaction, error) {
	switch {
	case !ident.Valid(id) || !ident.Valid(walletID) || !ident.Valid(playerID):
		return nil, invalidArg("ids must be canonical UUIDs")
	case !amount.Currency().Valid() || amount.Sign() <= 0:
		return nil, invalidArg("opening amount must be positive")
	case !validText(correlationID):
		return nil, invalidArg("correlation id must have 1 to 128 characters")
	case now.IsZero():
		return nil, invalidArg("now is required")
	}
	now = normalize(now)
	return &WagerTransaction{
		id: id, origin: OriginInternal, kind: KindOpening, status: StatusPending,
		walletID: walletID, playerID: playerID, money: amount,
		correlationID: correlationID, createdAt: now, updatedAt: now,
	}, nil
}

// Rehydrate rebuilds a transaction from persisted state, without transitions
// or events. It checks the constraints of data-model §3.2, so corrupted data is
// rejected with ErrInvalidSnapshot (a permanent failure). PENDING is never
// persisted, so it is rejected too.
func Rehydrate(s Snapshot) (*WagerTransaction, error) {
	if reason := snapshotProblem(s); reason != "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidSnapshot, reason)
	}
	return &WagerTransaction{
		id:                             s.ID,
		origin:                         s.Origin,
		kind:                           s.Kind,
		status:                         s.Status,
		walletID:                       s.WalletID,
		playerID:                       s.PlayerID,
		money:                          s.Money,
		providerID:                     s.ProviderID,
		externalTransactionID:          s.ExternalTransactionID,
		idempotencyKey:                 s.IdempotencyKey,
		payloadHash:                    s.PayloadHash,
		roundID:                        s.RoundID,
		gameID:                         s.GameID,
		referenceExternalTransactionID: s.ReferenceExternalTransactionID,
		receivedVia:                    s.ReceivedVia,
		referenceTransactionID:         s.ReferenceTransactionID,
		failureCode:                    s.FailureCode,
		resultBalance:                  s.ResultBalance,
		attempts:                       s.Attempts,
		nextAttemptAt:                  normalizeOptional(s.NextAttemptAt),
		expiresAt:                      normalizeOptional(s.ExpiresAt),
		correlationID:                  s.CorrelationID,
		createdAt:                      normalize(s.CreatedAt),
		updatedAt:                      normalize(s.UpdatedAt),
		completedAt:                    normalizeOptional(s.CompletedAt),
	}, nil
}

// snapshotProblem returns why s cannot be rehydrated, or "".
func snapshotProblem(s Snapshot) string {
	external := s.Origin == OriginExternal
	processed := s.Status == StatusProcessed
	switch {
	case !ident.Valid(s.ID) || !ident.Valid(s.WalletID) || !ident.Valid(s.PlayerID):
		return "ids must be canonical UUIDs"
	case !s.Origin.Valid() || !s.Kind.Valid() || !s.Status.Valid():
		return "unknown origin, kind or status"
	case s.Status == StatusPending:
		return "PENDING is never persisted"
	case !s.Money.Currency().Valid():
		return "money is uninitialized"
	case (s.Origin == OriginInternal) != (s.Kind == KindOpening):
		return "only OPENING is INTERNAL"
	case !external && (s.ProviderID != "" || s.ExternalTransactionID != "" || s.IdempotencyKey != "" ||
		s.PayloadHash != "" || s.RoundID != "" || s.GameID != "" || s.ReferenceExternalTransactionID != "" ||
		s.ReferenceTransactionID != "" || s.ReceivedVia != "" || !processed):
		return "an INTERNAL operation has no external fields and is PROCESSED"
	case external && (s.ProviderID == "" || s.ExternalTransactionID == "" || s.IdempotencyKey == "" ||
		!validHash(s.PayloadHash) || s.RoundID == "" || s.GameID == "" || !s.ReceivedVia.Valid()):
		return "an EXTERNAL operation needs every external field"
	case (s.Kind == KindLoss) != (s.Money.Sign() == 0) || s.Money.Sign() < 0:
		return "only LOSS has a zero amount"
	case s.Kind.referenceRule() == referenceRequired && s.ReferenceExternalTransactionID == "",
		s.Kind.referenceRule() == referenceForbidden && s.ReferenceExternalTransactionID != "":
		return "reference does not follow the kind policy"
	case processed && s.ReferenceExternalTransactionID != "" && !ident.Valid(s.ReferenceTransactionID),
		s.ReferenceTransactionID != "" && !ident.Valid(s.ReferenceTransactionID):
		return "a PROCESSED operation with a reference needs the resolved reference id"
	case (s.Status == StatusRejected || s.Status == StatusFailed) != (s.FailureCode != ""),
		s.Status == StatusRejected && !s.FailureCode.IsRejection(),
		s.Status == StatusFailed && s.FailureCode != FailureInternalPermanentFailure:
		return "failure code does not match the status"
	case (processed || s.Status == StatusRejected) && (!s.ResultBalance.Currency().Valid() || s.ResultBalance.Sign() < 0):
		return "PROCESSED and REJECTED need the observed balance"
	case s.Status.IsTerminal() == s.CompletedAt.IsZero():
		return "completed at is set only in terminal states"
	case s.Status == StatusPendingReference && (s.NextAttemptAt.IsZero() || s.ExpiresAt.IsZero() ||
		s.ReferenceExternalTransactionID == ""):
		return "PENDING_REFERENCE needs a reference and its retry schedule"
	case s.Attempts < 0:
		return "attempts must not be negative"
	case !validText(s.CorrelationID):
		return "correlation id must have 1 to 128 characters"
	case s.CreatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt):
		return "updated at must not precede created at"
	}
	return ""
}

func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Snapshot returns the state to persist. A PENDING transaction is never
// persisted (D-05).
func (t *WagerTransaction) Snapshot() (Snapshot, error) {
	if !t.initialized() {
		return Snapshot{}, fmt.Errorf("%w: wager transaction", ErrUninitialized)
	}
	if t.status == StatusPending {
		return Snapshot{}, ErrNotPersistable
	}
	return Snapshot{
		ID:                             t.id,
		Origin:                         t.origin,
		Kind:                           t.kind,
		Status:                         t.status,
		WalletID:                       t.walletID,
		PlayerID:                       t.playerID,
		Money:                          t.money,
		ProviderID:                     t.providerID,
		ExternalTransactionID:          t.externalTransactionID,
		IdempotencyKey:                 t.idempotencyKey,
		PayloadHash:                    t.payloadHash,
		RoundID:                        t.roundID,
		GameID:                         t.gameID,
		ReferenceExternalTransactionID: t.referenceExternalTransactionID,
		ReceivedVia:                    t.receivedVia,
		ReferenceTransactionID:         t.referenceTransactionID,
		FailureCode:                    t.failureCode,
		ResultBalance:                  t.resultBalance,
		Attempts:                       t.attempts,
		NextAttemptAt:                  t.nextAttemptAt,
		ExpiresAt:                      t.expiresAt,
		CorrelationID:                  t.correlationID,
		CreatedAt:                      t.createdAt,
		UpdatedAt:                      t.updatedAt,
		CompletedAt:                    t.completedAt,
	}, nil
}

func (t *WagerTransaction) ID() string                     { return t.id }
func (t *WagerTransaction) Origin() Origin                 { return t.origin }
func (t *WagerTransaction) Kind() Kind                     { return t.kind }
func (t *WagerTransaction) Status() Status                 { return t.status }
func (t *WagerTransaction) WalletID() string               { return t.walletID }
func (t *WagerTransaction) PlayerID() string               { return t.playerID }
func (t *WagerTransaction) Money() money.Money             { return t.money }
func (t *WagerTransaction) ProviderID() string             { return t.providerID }
func (t *WagerTransaction) ExternalTransactionID() string  { return t.externalTransactionID }
func (t *WagerTransaction) IdempotencyKey() string         { return t.idempotencyKey }
func (t *WagerTransaction) PayloadHash() string            { return t.payloadHash }
func (t *WagerTransaction) RoundID() string                { return t.roundID }
func (t *WagerTransaction) GameID() string                 { return t.gameID }
func (t *WagerTransaction) ReceivedVia() ReceivedVia       { return t.receivedVia }
func (t *WagerTransaction) ReferenceTransactionID() string { return t.referenceTransactionID }
func (t *WagerTransaction) FailureCode() FailureCode       { return t.failureCode }
func (t *WagerTransaction) Attempts() int                  { return t.attempts }
func (t *WagerTransaction) NextAttemptAt() time.Time       { return t.nextAttemptAt }
func (t *WagerTransaction) ExpiresAt() time.Time           { return t.expiresAt }
func (t *WagerTransaction) CorrelationID() string          { return t.correlationID }
func (t *WagerTransaction) CreatedAt() time.Time           { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time           { return t.updatedAt }
func (t *WagerTransaction) CompletedAt() time.Time         { return t.completedAt }

// ReferenceExternalTransactionID returns "" when the operation has no reference.
func (t *WagerTransaction) ReferenceExternalTransactionID() string {
	return t.referenceExternalTransactionID
}

// ResultBalance is the balance observed when the operation completed and the
// one returned by replays (IDEM-08). It is the zero value while not completed
// and for FAILED.
func (t *WagerTransaction) ResultBalance() money.Money { return t.resultBalance }

func (t *WagerTransaction) initialized() bool { return t != nil && t.id != "" }

func (t *WagerTransaction) hasReference() bool { return t.referenceExternalTransactionID != "" }

// normalize keeps instants in UTC with microsecond precision, the precision of
// PostgreSQL (data-model §2).
func normalize(t time.Time) time.Time { return t.UTC().Truncate(time.Microsecond) }

// notBefore returns t, or floor when t precedes it. Another instance's clock
// may lag behind the creation instant; updatedAt must still not precede
// createdAt, or the snapshot would not rehydrate.
func notBefore(t, floor time.Time) time.Time {
	if t.Before(floor) {
		return floor
	}
	return t
}

func normalizeOptional(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	return normalize(t)
}
