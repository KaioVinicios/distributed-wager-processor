package wagering

import (
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// movement is the wallet movement of the kind (lifecycle §2). ROLLBACK moves
// against its reference: it credits a BET and debits a WIN or a REFUND.
func (t *WagerTransaction) movement(ref *WagerTransaction) (wallet.Direction, bool) {
	switch t.kind {
	case KindBet:
		return wallet.DirectionDebit, true
	case KindOpening, KindWin, KindRefund:
		return wallet.DirectionCredit, true
	case KindRollback:
		if ref != nil && ref.kind == KindBet {
			return wallet.DirectionCredit, true
		}
		return wallet.DirectionDebit, true
	case KindLoss:
		return "", false
	}
	return "", false
}

// referenceRejection checks a found reference (R3–R6 of lifecycle §4) and
// returns the rejection code, or "" when it is acceptable. R1/R2 (not
// resolved yet) and R7 (already reversed) are decided by Settle.
func (t *WagerTransaction) referenceRejection(ref *WagerTransaction) FailureCode {
	switch {
	case ref.status == StatusRejected || ref.status == StatusFailed:
		return FailureReferenceNotProcessed
	case !t.kind.accepts(ref.kind):
		return FailureInvalidReferenceKind
	case ref.playerID != t.playerID, ref.walletID != t.walletID, ref.roundID != t.roundID,
		ref.money.Currency() != t.money.Currency():
		return FailureReferenceMismatch
	case t.kind.isReversal() && ref.money.Minor() != t.money.Minor():
		return FailureReversalAmountMismatch
	}
	return ""
}

func nonNegative(m money.Money) bool { return m.Currency().Valid() && m.Sign() >= 0 }
