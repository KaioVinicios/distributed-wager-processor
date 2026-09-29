package apperrors_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/KaioVinicios/pda/internal/apperrors"
)

func TestClassify(t *testing.T) {
	// Covers: TX-10, DOM-04
	boom := errors.New("boom")
	kinds := []apperrors.Kind{
		apperrors.KindInput, apperrors.KindNotFound, apperrors.KindForbidden, apperrors.KindConflict,
		apperrors.KindBusiness, apperrors.KindTransient, apperrors.KindPermanent,
	}
	for _, k := range kinds {
		direct := apperrors.New(k, "SOME_CODE", boom)
		wrapped := fmt.Errorf("use case: %w", fmt.Errorf("repository: %w", direct))
		if got := apperrors.Classify(direct); got != k {
			t.Errorf("Classify(%s) = %s", k, got)
		}
		if got := apperrors.Classify(wrapped); got != k {
			t.Errorf("Classify(wrapped %s) = %s", k, got)
		}
		if !errors.Is(wrapped, boom) {
			t.Errorf("%s: the cause must stay reachable", k)
		}
	}

	outer := apperrors.New(apperrors.KindConflict, "IDEMPOTENCY_KEY_REUSED", apperrors.New(apperrors.KindTransient, "", boom))
	if got := apperrors.Classify(outer); got != apperrors.KindConflict {
		t.Errorf("the outermost classification wins, got %s", got)
	}
	joined := errors.Join(boom, apperrors.New(apperrors.KindPermanent, "", nil))
	if got := apperrors.Classify(joined); got != apperrors.KindPermanent {
		t.Errorf("Classify(errors.Join) = %s, want PERMANENT", got)
	}

	transient := map[string]error{
		"context.Canceled":         context.Canceled,
		"context.DeadlineExceeded": context.DeadlineExceeded,
		"wrapped deadline":         fmt.Errorf("query: %w", context.DeadlineExceeded),
		"unclassified (D-05)":      boom,
	}
	for name, err := range transient {
		if got := apperrors.Classify(err); got != apperrors.KindTransient {
			t.Errorf("%s: Classify = %s, want TRANSIENT", name, got)
		}
	}
	if got := apperrors.Classify(nil); got != "" {
		t.Errorf("Classify(nil) = %q, want empty", got)
	}
}

func TestCodeOf(t *testing.T) {
	// Covers: OPS-15
	err := fmt.Errorf("handler: %w", apperrors.New(apperrors.KindInput, "UNKNOWN_WALLET", nil))
	if got := apperrors.CodeOf(err); got != "UNKNOWN_WALLET" {
		t.Errorf("CodeOf = %q", got)
	}
	if got := apperrors.CodeOf(errors.New("x")); got != "" {
		t.Errorf("CodeOf(unclassified) = %q, want empty", got)
	}
	e := apperrors.New(apperrors.KindInput, "UNKNOWN_WALLET", errors.New("wallet 1"))
	if e == nil || e.Error() != "INPUT UNKNOWN_WALLET: wallet 1" {
		t.Errorf("Error() of %#v", e)
	}
}
