-- data-model.md §5 (D-17). The roles are created by deploy/postgres/01-roles.sh.
GRANT SELECT, INSERT, UPDATE ON wallets TO pda_app;
GRANT SELECT, INSERT, UPDATE ON wager_transactions TO pda_app;
GRANT SELECT, INSERT ON wallet_ledger_entries TO pda_app;
GRANT SELECT, INSERT ON inbox_messages TO pda_app;
GRANT SELECT, INSERT, UPDATE ON outbox_events TO pda_app;
