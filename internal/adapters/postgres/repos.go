package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KaioVinicios/pda/internal/app"
)

// repos binds every repository to one querier.
type repos struct{ q querier }

var _ app.Repos = repos{}

// NewRepos returns the repositories over the pool, for reads outside a
// transaction (D-14). Writes belong in UnitOfWork.Do.
func NewRepos(pool *pgxpool.Pool) app.Repos { return repos{q: pool} }

func (r repos) Wallets() app.WalletRepository           { return walletRepo(r) }
func (r repos) Transactions() app.TransactionRepository { return transactionRepo(r) }
func (r repos) Ledger() app.LedgerRepository            { return ledgerRepo(r) }
func (r repos) Outbox() app.OutboxRepository            { return outboxRepo(r) }
func (r repos) Inbox() app.InboxRepository              { return inboxRepo(r) }
