package wagering

import (
	"time"

	"github.com/KaioVinicios/pda/internal/domain/events"
	"github.com/KaioVinicios/pda/internal/domain/money"
	"github.com/KaioVinicios/pda/internal/domain/wallet"
)

// OpenParams are the inputs of a wallet opening (POST /wallets).
type OpenParams struct {
	WalletID string
	PlayerID string
	Initial  money.Money
	// TransactionID and EntryID identify the OPENING and its ledger entry; they
	// are ignored when the initial balance is zero.
	TransactionID string
	EntryID       string
	CorrelationID string
	Now           time.Time
}

// Opening is what the caller persists in one SQL transaction: the wallet and,
// for a positive balance, the OPENING, its entry and its events.
type Opening struct {
	Wallet wallet.Wallet
	Tx     *WagerTransaction
	Entry  *wallet.LedgerEntry
	Events []events.Event
}

// OpenWallet opens a wallet (lifecycle §6.4, CHALLENGE §9). A positive initial
// balance creates the INTERNAL OPENING, PENDING → PROCESSED through the same
// state machine, its CREDIT entry from 0.00 at version 1 and the events
// WagerTransactionProcessed + WalletBalanceChanged. A zero balance creates no
// OPENING, no entry and no event (HTTP-01).
func OpenWallet(p OpenParams) (Opening, error) {
	w, err := wallet.Open(p.WalletID, p.PlayerID, p.Initial, p.Now)
	if err != nil {
		return Opening{}, err
	}
	if w.Balance().Sign() == 0 {
		return Opening{Wallet: w}, nil
	}
	tx, err := NewOpening(p.TransactionID, p.WalletID, p.PlayerID, p.Initial, p.CorrelationID, p.Now)
	if err != nil {
		return Opening{}, err
	}
	entry, err := w.OpeningEntry(p.EntryID, tx.id)
	if err != nil {
		return Opening{}, err
	}
	evs, err := tx.Process(ProcessParams{Entry: &entry, Balance: w.Balance(), WalletVersion: w.Version(), Now: p.Now})
	if err != nil {
		return Opening{}, err
	}
	return Opening{Wallet: w, Tx: tx, Entry: &entry, Events: evs}, nil
}
