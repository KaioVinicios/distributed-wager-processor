package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/money"
)

// Reconciliation compares the stored balance with the ledger (HTTP-07).
type Reconciliation struct {
	WalletID                       string
	Stored, Calculated, Difference money.Money
	Consistent                     bool
	CheckedEntries                 int64
}

// Reconcile rebuilds the balance from the ledger without changing anything.
type Reconcile struct {
	uow     UnitOfWork
	metrics Metrics
	log     *slog.Logger
}

// NewReconcile builds the use case.
func NewReconcile(uow UnitOfWork, metrics Metrics, log *slog.Logger) *Reconcile {
	return &Reconcile{uow: uow, metrics: metrics, log: log}
}

// Execute reads the wallet and the ledger sum in one REPEATABLE READ READ ONLY
// snapshot (D-16) and returns difference = stored − calculated. A divergence
// is logged and counted, never corrected.
func (r *Reconcile) Execute(ctx context.Context, walletID, correlationID string) (Reconciliation, error) {
	var out Reconciliation
	err := r.uow.Snapshot(ctx, func(repos Repos) error {
		w, err := repos.Wallets().Get(ctx, walletID)
		if errors.Is(err, ErrNotFound) {
			return apperrors.New(apperrors.KindNotFound, CodeWalletNotFound, err)
		}
		if err != nil {
			return err
		}
		sum, err := repos.Ledger().Sum(ctx, w.ID())
		if err != nil {
			return err
		}
		calculated, err := money.FromMinor(sum.NetMinor, w.Currency())
		if err != nil {
			return domainError(err)
		}
		difference, err := w.Balance().Sub(calculated)
		if err != nil {
			return domainError(err)
		}
		out = Reconciliation{
			WalletID: w.ID(), Stored: w.Balance(), Calculated: calculated, Difference: difference,
			Consistent: difference.Sign() == 0, CheckedEntries: sum.Entries,
		}
		return nil
	})
	if err != nil {
		return Reconciliation{}, err
	}
	if !out.Consistent {
		r.log.WarnContext(ctx, "reconciliation divergence",
			"walletId", out.WalletID, "correlationId", correlationID,
			"storedBalance", out.Stored.String(), "calculatedBalance", out.Calculated.String(),
			"difference", out.Difference.String())
		r.metrics.ReconciliationDivergence()
	}
	return out, nil
}
