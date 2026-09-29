-- data-model.md §3.2
CREATE TABLE wager_transactions (
    id                                UUID        PRIMARY KEY,
    origin                            TEXT        NOT NULL CHECK (origin IN ('INTERNAL','EXTERNAL')),
    kind                              TEXT        NOT NULL CHECK (kind IN ('OPENING','BET','WIN','LOSS','REFUND','ROLLBACK')),
    status                            TEXT        NOT NULL CHECK (status IN ('PENDING_REFERENCE','PROCESSED','REJECTED','FAILED')),
    wallet_id                         UUID        NOT NULL REFERENCES wallets(id),
    player_id                         UUID        NOT NULL,
    amount_minor                      BIGINT      NOT NULL CHECK (amount_minor >= 0),
    currency                          CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

    -- External metadata (NULL for OPENING)
    provider_id                       TEXT,
    external_transaction_id           TEXT,
    idempotency_key                   TEXT,
    payload_hash                      CHAR(64)    CHECK (payload_hash ~ '^[0-9a-f]{64}$'),
    round_id                          TEXT,
    game_id                           TEXT,
    reference_external_transaction_id TEXT,
    received_via                      TEXT        CHECK (received_via IN ('HTTP','SQS')),

    -- Result
    reference_transaction_id          UUID        REFERENCES wager_transactions(id),
    failure_code                      TEXT,
    result_balance_minor              BIGINT      CHECK (result_balance_minor >= 0),

    -- Reference resolution schedule (D-11)
    attempts                          INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at                   TIMESTAMPTZ,
    expires_at                        TIMESTAMPTZ,

    correlation_id                    TEXT        NOT NULL,
    created_at                        TIMESTAMPTZ NOT NULL,
    updated_at                        TIMESTAMPTZ NOT NULL,
    completed_at                      TIMESTAMPTZ,

    -- Target of the ledger's composite FK
    CONSTRAINT wager_tx_id_wallet_uq UNIQUE (id, wallet_id),

    -- Origin × kind (TX-01, TX-04, TX-05)
    CONSTRAINT wager_tx_origin_kind CHECK ((origin = 'INTERNAL') = (kind = 'OPENING')),
    CONSTRAINT wager_tx_internal_fields CHECK (
        origin <> 'INTERNAL' OR (
            provider_id IS NULL AND external_transaction_id IS NULL AND idempotency_key IS NULL
            AND payload_hash IS NULL AND round_id IS NULL AND game_id IS NULL
            AND reference_external_transaction_id IS NULL AND reference_transaction_id IS NULL
            AND received_via IS NULL
            AND status = 'PROCESSED' AND amount_minor > 0
        )
    ),
    CONSTRAINT wager_tx_external_fields CHECK (
        origin <> 'EXTERNAL' OR (
            provider_id IS NOT NULL AND external_transaction_id IS NOT NULL
            AND idempotency_key IS NOT NULL AND payload_hash IS NOT NULL
            AND round_id IS NOT NULL AND game_id IS NOT NULL AND received_via IS NOT NULL
        )
    ),

    -- Amount policy by kind (OPS-03, OPS-15)
    CONSTRAINT wager_tx_amount_policy CHECK (
        (kind = 'LOSS' AND amount_minor = 0) OR (kind <> 'LOSS' AND amount_minor > 0)
    ),

    -- References by kind (OPS-06)
    CONSTRAINT wager_tx_reference_policy CHECK (
        CASE kind
            WHEN 'REFUND'   THEN reference_external_transaction_id IS NOT NULL
            WHEN 'ROLLBACK' THEN reference_external_transaction_id IS NOT NULL
            WHEN 'WIN'      THEN TRUE
            ELSE reference_external_transaction_id IS NULL
        END
    ),
    CONSTRAINT wager_tx_resolved_reference CHECK (
        NOT (status = 'PROCESSED' AND kind IN ('REFUND','ROLLBACK')) OR reference_transaction_id IS NOT NULL
    ),
    CONSTRAINT wager_tx_resolved_win_reference CHECK (
        NOT (status = 'PROCESSED' AND kind = 'WIN' AND reference_external_transaction_id IS NOT NULL)
        OR reference_transaction_id IS NOT NULL
    ),

    -- State × result (TX-03, TX-06)
    CONSTRAINT wager_tx_failure_code CHECK (
        (status IN ('REJECTED','FAILED')) = (failure_code IS NOT NULL)
    ),
    CONSTRAINT wager_tx_result_balance CHECK (
        status NOT IN ('PROCESSED','REJECTED') OR result_balance_minor IS NOT NULL
    ),
    CONSTRAINT wager_tx_completed_at CHECK (
        (status IN ('PROCESSED','REJECTED','FAILED')) = (completed_at IS NOT NULL)
    ),
    CONSTRAINT wager_tx_pending_schedule CHECK (
        status <> 'PENDING_REFERENCE' OR (next_attempt_at IS NOT NULL AND expires_at IS NOT NULL)
    )
);

-- Idempotency (IDEM-02, IDEM-07)
CREATE UNIQUE INDEX wager_tx_idempotency_uq
    ON wager_transactions (provider_id, idempotency_key) WHERE origin = 'EXTERNAL';
CREATE UNIQUE INDEX wager_tx_external_id_uq
    ON wager_transactions (provider_id, external_transaction_id) WHERE origin = 'EXTERNAL';

-- A single opening per wallet (TX-05)
CREATE UNIQUE INDEX wager_tx_single_opening_uq
    ON wager_transactions (wallet_id) WHERE kind = 'OPENING';

-- One successful compensation per reference (OPS-08, D-10)
CREATE UNIQUE INDEX wager_tx_single_reversal_uq
    ON wager_transactions (reference_transaction_id)
    WHERE kind IN ('REFUND','ROLLBACK') AND status = 'PROCESSED';

-- Reference worker (D-11)
CREATE INDEX wager_tx_pending_due_idx
    ON wager_transactions (next_attempt_at) WHERE status = 'PENDING_REFERENCE';
CREATE INDEX wager_tx_pending_by_reference_idx
    ON wager_transactions (provider_id, reference_external_transaction_id)
    WHERE status = 'PENDING_REFERENCE';

-- Queries by wallet
CREATE INDEX wager_tx_wallet_created_idx ON wager_transactions (wallet_id, created_at);
