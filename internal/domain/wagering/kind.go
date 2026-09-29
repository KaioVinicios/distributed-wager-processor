package wagering

import "fmt"

// Kind is the operation type (lifecycle §2).
type Kind string

const (
	KindOpening  Kind = "OPENING"
	KindBet      Kind = "BET"
	KindWin      Kind = "WIN"
	KindLoss     Kind = "LOSS"
	KindRefund   Kind = "REFUND"
	KindRollback Kind = "ROLLBACK"
)

// ParseKind accepts the exact (case-sensitive) name of a kind, OPENING
// included; the command rejects OPENING separately.
func ParseKind(s string) (Kind, error) {
	k := Kind(s)
	if !k.Valid() {
		return "", fmt.Errorf("%w: kind", ErrInvalidEnum)
	}
	return k, nil
}

// Valid reports whether k is a known kind. The zero value is not.
func (k Kind) Valid() bool {
	switch k {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	}
	return false
}

type referenceRule int

const (
	referenceForbidden referenceRule = iota
	referenceOptional
	referenceRequired
)

// referenceRule is the reference policy of step 7 (lifecycle §3.1).
func (k Kind) referenceRule() referenceRule {
	switch k {
	case KindRefund, KindRollback:
		return referenceRequired
	case KindWin:
		return referenceOptional
	case KindOpening, KindBet, KindLoss:
		return referenceForbidden
	}
	return referenceForbidden
}

// isReversal reports whether k compensates a previous operation (D-10).
func (k Kind) isReversal() bool { return k == KindRefund || k == KindRollback }

// accepts reports whether k may reference a transaction of kind ref (R4).
func (k Kind) accepts(ref Kind) bool {
	switch k {
	case KindWin, KindRefund:
		return ref == KindBet
	case KindRollback:
		return ref == KindBet || ref == KindWin || ref == KindRefund
	case KindOpening, KindBet, KindLoss:
		return false
	}
	return false
}
