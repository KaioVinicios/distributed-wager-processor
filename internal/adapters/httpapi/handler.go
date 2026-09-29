package httpapi

import (
	"context"
	"log/slog"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
	"github.com/KaioVinicios/pda/internal/observability"
)

// Authenticator verifies bearer tokens (auth.Verifier).
type Authenticator interface {
	Authenticate(ctx context.Context, raw string) (auth.Principal, error)
}

// WagerProcessor processes an operation (app.ProcessWager).
type WagerProcessor interface {
	Execute(ctx context.Context, req app.ProcessRequest) (app.ProcessResult, error)
}

// WalletOpener opens a wallet (app.OpenWallet).
type WalletOpener interface {
	Execute(ctx context.Context, in app.OpenWalletInput, correlationID string) (wallet.Wallet, error)
}

// Reader answers the queries (app.Queries).
type Reader interface {
	GetWallet(ctx context.Context, id string) (wallet.Wallet, error)
	ListLedger(ctx context.Context, walletID, cursor string, limit int) (app.LedgerPage, error)
	GetTransaction(ctx context.Context, id string) (*wagering.WagerTransaction, error)
	GetTransactionByExternalID(ctx context.Context, providerID, externalID string) (*wagering.WagerTransaction, error)
}

// Reconciler reconciles a wallet (app.Reconcile).
type Reconciler interface {
	Execute(ctx context.Context, walletID, correlationID string) (app.Reconciliation, error)
}

// Services are what the routes call. The handlers depend on these small
// interfaces, satisfied by the use cases and by stubs in the unit tests.
type Services struct {
	Auth      Authenticator
	Wagers    WagerProcessor
	Wallets   WalletOpener
	Queries   Reader
	Reconcile Reconciler
	Health    *observability.Health
}

// Options configure the handler.
type Options struct {
	DocsEnabled bool
	Log         *slog.Logger
}

// handlers are the business routes.
type handlers struct {
	s   Services
	log *slog.Logger
}
