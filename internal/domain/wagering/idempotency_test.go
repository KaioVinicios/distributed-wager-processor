package wagering_test

import (
	"errors"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

func TestIdempotencyDecision(t *testing.T) {
	// Covers: TST-U05, IDEM-05, IDEM-06, IDEM-07
	stored := rehydrate(t, referenceSnapshot(t, wagering.KindBet, "25.00"))
	same, other := stored.PayloadHash(), mustCommand(t, betInput()).PayloadHash()

	replay, err := wagering.CheckIdempotency(same, stored, stored)
	if err != nil || replay != stored {
		t.Fatalf("same key, same hash: %v, %v; want replay", replay, err)
	}

	conflicts := map[string]struct {
		byKey, byExternalID *wagering.WagerTransaction
		want                wagering.InputCode
	}{
		"same key, other hash":           {stored, nil, wagering.InputIdempotencyKeyReused},
		"other key, same external id":    {nil, stored, wagering.InputExternalTransactionIDConflict},
		"same key wins over external id": {stored, stored, wagering.InputIdempotencyKeyReused},
	}
	for name, c := range conflicts {
		replay, err := wagering.CheckIdempotency(other, c.byKey, c.byExternalID)
		var ce *wagering.ConflictError
		if replay != nil || !errors.As(err, &ce) || ce.Code != c.want {
			t.Errorf("%s: %v, %v; want ConflictError %s", name, replay, err, c.want)
		}
	}

	if replay, err := wagering.CheckIdempotency(other, nil, nil); replay != nil || err != nil {
		t.Fatalf("nothing found: %v, %v; want nil, nil", replay, err)
	}
}
