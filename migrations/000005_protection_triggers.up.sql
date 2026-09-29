-- data-model.md §4. The triggers also apply to the table owner.

-- §4.1 Append-only ledger (LED-04): PDA01
CREATE FUNCTION ledger_block_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet_ledger_entries is append-only (% blocked)', TG_OP
        USING ERRCODE = 'PDA01';
END $$;

CREATE TRIGGER ledger_no_update_delete BEFORE UPDATE OR DELETE ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_block_mutation();
CREATE TRIGGER ledger_no_truncate BEFORE TRUNCATE ON wallet_ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_block_mutation();

-- §4.2 Ledger × wallet × transaction coherence (WAL-06, LED-05): PDA04
CREATE FUNCTION ledger_check_wallet() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    w        wallets%ROWTYPE;
    t        wager_transactions%ROWTYPE;
    expected TEXT;
BEGIN
    -- 1. The entry reflects the current wallet state
    SELECT * INTO w FROM wallets WHERE id = NEW.wallet_id;
    IF NEW.currency <> w.currency
       OR NEW.balance_after_minor <> w.balance_minor
       OR NEW.wallet_version <> w.version THEN
        RAISE EXCEPTION 'ledger entry does not match wallet % state', NEW.wallet_id
            USING ERRCODE = 'PDA04';
    END IF;

    -- 2. Only PROCESSED transactions that move the balance get an entry, with the same amount and currency
    SELECT * INTO t FROM wager_transactions WHERE id = NEW.transaction_id;
    IF t.status <> 'PROCESSED' OR t.kind = 'LOSS'
       OR NEW.amount_minor <> t.amount_minor OR NEW.currency <> t.currency THEN
        RAISE EXCEPTION 'ledger entry does not match transaction %', NEW.transaction_id
            USING ERRCODE = 'PDA04';
    END IF;

    -- 3. The direction matches the kind (a ROLLBACK depends on its reference's kind)
    expected := CASE t.kind
        WHEN 'BET' THEN 'DEBIT'
        WHEN 'ROLLBACK' THEN (
            SELECT CASE r.kind WHEN 'BET' THEN 'CREDIT' ELSE 'DEBIT' END
            FROM wager_transactions r WHERE r.id = t.reference_transaction_id)
        ELSE 'CREDIT'  -- OPENING, WIN, REFUND
    END;
    IF NEW.direction IS DISTINCT FROM expected THEN
        RAISE EXCEPTION 'ledger direction % does not match transaction kind %', NEW.direction, t.kind
            USING ERRCODE = 'PDA04';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER ledger_matches_wallet BEFORE INSERT ON wallet_ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_check_wallet();

-- Checked at commit: every balance change has the entry of the new version
CREATE FUNCTION wallet_require_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (TG_OP = 'INSERT' AND NEW.balance_minor > 0)
       OR (TG_OP = 'UPDATE' AND NEW.balance_minor <> OLD.balance_minor) THEN
        IF NOT EXISTS (
            SELECT 1 FROM wallet_ledger_entries
            WHERE wallet_id = NEW.id AND wallet_version = NEW.version
              AND balance_after_minor = NEW.balance_minor
        ) THEN
            RAISE EXCEPTION 'wallet % balance changed without ledger entry', NEW.id
                USING ERRCODE = 'PDA04';
        END IF;
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER wallet_balance_has_ledger
    AFTER INSERT OR UPDATE ON wallets
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION wallet_require_ledger();

-- Checked at commit: every PROCESSED transaction that moves the balance has its entry
CREATE FUNCTION wager_tx_require_ledger() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM wallet_ledger_entries
        WHERE wallet_id = NEW.wallet_id AND transaction_id = NEW.id
    ) THEN
        RAISE EXCEPTION 'processed transaction % has no ledger entry', NEW.id
            USING ERRCODE = 'PDA04';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER wager_tx_processed_has_ledger
    AFTER INSERT OR UPDATE OF status ON wager_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    WHEN (NEW.status = 'PROCESSED' AND NEW.kind <> 'LOSS')
    EXECUTE FUNCTION wager_tx_require_ledger();

-- §4.3 Wallet: version and immutable columns (WAL-07): PDA03
CREATE FUNCTION wallet_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.id, NEW.player_id, NEW.currency, NEW.created_at)
       IS DISTINCT FROM (OLD.id, OLD.player_id, OLD.currency, OLD.created_at) THEN
        RAISE EXCEPTION 'wallet % immutable column changed', OLD.id USING ERRCODE = 'PDA03';
    END IF;
    IF NEW.balance_minor <> OLD.balance_minor AND NEW.version <> OLD.version + 1 THEN
        RAISE EXCEPTION 'wallet % balance change requires version + 1', OLD.id USING ERRCODE = 'PDA03';
    END IF;
    IF NEW.balance_minor = OLD.balance_minor AND NEW.version <> OLD.version THEN
        RAISE EXCEPTION 'wallet % version changed without balance change', OLD.id USING ERRCODE = 'PDA03';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION wallet_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wallet % cannot be deleted', OLD.id USING ERRCODE = 'PDA03';
END $$;

CREATE TRIGGER wallet_guard BEFORE UPDATE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallet_guard_update();
CREATE TRIGGER wallet_no_delete BEFORE DELETE ON wallets
    FOR EACH ROW EXECUTE FUNCTION wallet_guard_delete();

-- §4.4 Transaction: terminal states and immutable columns (TX-07): PDA02
CREATE FUNCTION wager_tx_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('PROCESSED','REJECTED','FAILED') THEN
        RAISE EXCEPTION 'wager transaction % is terminal (%)', OLD.id, OLD.status
            USING ERRCODE = 'PDA02';
    END IF;
    IF (NEW.id, NEW.origin, NEW.kind, NEW.wallet_id, NEW.player_id, NEW.amount_minor,
        NEW.currency, NEW.provider_id, NEW.external_transaction_id, NEW.idempotency_key,
        NEW.payload_hash, NEW.round_id, NEW.game_id, NEW.reference_external_transaction_id,
        NEW.received_via, NEW.correlation_id, NEW.created_at)
       IS DISTINCT FROM
       (OLD.id, OLD.origin, OLD.kind, OLD.wallet_id, OLD.player_id, OLD.amount_minor,
        OLD.currency, OLD.provider_id, OLD.external_transaction_id, OLD.idempotency_key,
        OLD.payload_hash, OLD.round_id, OLD.game_id, OLD.reference_external_transaction_id,
        OLD.received_via, OLD.correlation_id, OLD.created_at) THEN
        RAISE EXCEPTION 'wager transaction % immutable column changed', OLD.id
            USING ERRCODE = 'PDA02';
    END IF;
    RETURN NEW;
END $$;

CREATE FUNCTION wager_tx_guard_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'wager transaction % cannot be deleted', OLD.id USING ERRCODE = 'PDA02';
END $$;

CREATE TRIGGER wager_tx_guard BEFORE UPDATE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_tx_guard_update();
CREATE TRIGGER wager_tx_no_delete BEFORE DELETE ON wager_transactions
    FOR EACH ROW EXECUTE FUNCTION wager_tx_guard_delete();

-- §4.5 Outbox: immutable snapshot (OUT-01): PDA05
CREATE FUNCTION outbox_guard_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.event_id, NEW.aggregate_type, NEW.aggregate_id, NEW.message_group_id, NEW.event_type,
        NEW.event_version, NEW.payload, NEW.correlation_id, NEW.causation_id, NEW.occurred_at)
       IS DISTINCT FROM
       (OLD.event_id, OLD.aggregate_type, OLD.aggregate_id, OLD.message_group_id, OLD.event_type,
        OLD.event_version, OLD.payload, OLD.correlation_id, OLD.causation_id, OLD.occurred_at) THEN
        RAISE EXCEPTION 'outbox event % snapshot is immutable', OLD.event_id USING ERRCODE = 'PDA05';
    END IF;
    IF OLD.published_at IS NOT NULL AND NEW.published_at IS NULL THEN
        RAISE EXCEPTION 'outbox event % cannot be unpublished', OLD.event_id USING ERRCODE = 'PDA05';
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER outbox_guard BEFORE UPDATE ON outbox_events
    FOR EACH ROW EXECUTE FUNCTION outbox_guard_update();
