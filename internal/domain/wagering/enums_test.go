package wagering_test

import (
	"errors"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

func TestParseEnums(t *testing.T) {
	// Covers: TX-01, TX-06, DOM-03
	valid := map[string]func(string) error{
		"OPENING":           func(s string) error { _, err := wagering.ParseKind(s); return err },
		"ROLLBACK":          func(s string) error { _, err := wagering.ParseKind(s); return err },
		"PENDING":           func(s string) error { _, err := wagering.ParseStatus(s); return err },
		"PENDING_REFERENCE": func(s string) error { _, err := wagering.ParseStatus(s); return err },
		"INTERNAL":          func(s string) error { _, err := wagering.ParseOrigin(s); return err },
		"SQS":               func(s string) error { _, err := wagering.ParseReceivedVia(s); return err },
		"ALREADY_REVERSED":  func(s string) error { _, err := wagering.ParseFailureCode(s); return err },
	}
	for in, parse := range valid {
		if err := parse(in); err != nil {
			t.Errorf("parse %q: %v", in, err)
		}
	}

	invalid := map[string]func() error{
		`Kind("")`:           func() error { _, err := wagering.ParseKind(""); return err },
		"lowercase kind":     func() error { _, err := wagering.ParseKind("bet"); return err },
		`Status("")`:         func() error { _, err := wagering.ParseStatus(""); return err },
		"unknown status":     func() error { _, err := wagering.ParseStatus("DONE"); return err },
		`Origin("")`:         func() error { _, err := wagering.ParseOrigin(""); return err },
		`ReceivedVia("")`:    func() error { _, err := wagering.ParseReceivedVia(""); return err },
		"lowercase channel":  func() error { _, err := wagering.ParseReceivedVia("http"); return err },
		`FailureCode("")`:    func() error { _, err := wagering.ParseFailureCode(""); return err },
		"input code as fail": func() error { _, err := wagering.ParseFailureCode("MISSING_FIELD"); return err },
	}
	for name, op := range invalid {
		if err := op(); !errors.Is(err, wagering.ErrInvalidEnum) {
			t.Errorf("%s: error = %v, want ErrInvalidEnum", name, err)
		}
	}

	if wagering.Kind("").Valid() || wagering.Status("").Valid() || wagering.Origin("").Valid() ||
		wagering.ReceivedVia("").Valid() || wagering.FailureCode("").Valid() {
		t.Error("zero values of the enums must be invalid")
	}
	for _, s := range []wagering.Status{wagering.StatusProcessed, wagering.StatusRejected, wagering.StatusFailed} {
		if !s.IsTerminal() {
			t.Errorf("%s must be terminal", s)
		}
	}
	for _, s := range []wagering.Status{wagering.StatusPending, wagering.StatusPendingReference} {
		if s.IsTerminal() {
			t.Errorf("%s must not be terminal", s)
		}
	}
}

func TestFailureCatalog(t *testing.T) {
	// Covers: OPS-15, OPS-10
	want := map[wagering.FailureCode]wagering.FailureCategory{
		wagering.FailureInsufficientFunds:         wagering.CategoryDefinitive,
		wagering.FailureReversalInsufficientFunds: wagering.CategoryDefinitive,
		wagering.FailureAlreadyReversed:           wagering.CategoryDefinitive,
		wagering.FailureReferenceNotFound:         wagering.CategoryDefinitive,
		wagering.FailureReferenceNotProcessed:     wagering.CategoryDefinitive,
		wagering.FailurePlayerWalletMismatch:      wagering.CategoryCorrectable,
		wagering.FailureCurrencyMismatch:          wagering.CategoryCorrectable,
		wagering.FailureReferenceMismatch:         wagering.CategoryCorrectable,
		wagering.FailureReversalAmountMismatch:    wagering.CategoryCorrectable,
		wagering.FailureInvalidReferenceKind:      wagering.CategoryCorrectable,
		wagering.FailureInternalPermanentFailure:  wagering.CategoryDefinitive,
	}
	for code, cat := range want {
		if got := code.Category(); got != cat {
			t.Errorf("%s.Category() = %q, want %q", code, got, cat)
		}
		if isRejection := code != wagering.FailureInternalPermanentFailure; code.IsRejection() != isRejection {
			t.Errorf("%s.IsRejection() = %v, want %v", code, code.IsRejection(), isRejection)
		}
	}
	if wagering.FailureInsufficientFunds == wagering.FailureReversalInsufficientFunds {
		t.Error("OPS-10: reversal without funds needs its own code")
	}
	if wagering.FailureCode("NOPE").Category() != "" || wagering.FailureCode("NOPE").IsRejection() {
		t.Error("unknown code must have no category")
	}
	if wagering.InputMissingField.Category() != wagering.CategoryCorrectable {
		t.Error("input codes are CORRECTABLE")
	}
}
