-- data-model.md §3.4
CREATE TABLE inbox_messages (
    consumer_name  TEXT        NOT NULL,
    message_id     TEXT        NOT NULL,
    message_hash   CHAR(64)    NOT NULL CHECK (message_hash ~ '^[0-9a-f]{64}$'),
    message_type   TEXT        NOT NULL,
    transaction_id UUID        REFERENCES wager_transactions(id),
    outcome        TEXT        NOT NULL CHECK (outcome IN ('PROCESSED','REJECTED','PENDING_REFERENCE','IDEMPOTENT_REPLAY','FAILED')),
    received_at    TIMESTAMPTZ NOT NULL,
    processed_at   TIMESTAMPTZ NOT NULL,
    CONSTRAINT inbox_pk PRIMARY KEY (consumer_name, message_id)   -- SQS-03
);

-- data-model.md §3.5
CREATE TABLE outbox_events (
    event_id         UUID        PRIMARY KEY,
    aggregate_type   TEXT        NOT NULL CHECK (aggregate_type IN ('Wallet','WagerTransaction')),
    aggregate_id     TEXT        NOT NULL,
    message_group_id TEXT        NOT NULL,   -- always the walletId (SNS MessageGroupId)
    event_type       TEXT        NOT NULL CHECK (event_type IN (
                         'WagerTransactionProcessed','WagerTransactionRejected',
                         'WalletBalanceChanged','WagerTransactionPendingReference')),
    event_version    INT         NOT NULL CHECK (event_version >= 1),
    payload          JSONB       NOT NULL,
    correlation_id   TEXT        NOT NULL,
    causation_id     TEXT,
    occurred_at      TIMESTAMPTZ NOT NULL,

    -- Publication control (D-13)
    attempts         INT         NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at  TIMESTAMPTZ NOT NULL,
    locked_by        TEXT,
    locked_until     TIMESTAMPTZ,
    published_at     TIMESTAMPTZ,
    last_error       TEXT,

    CONSTRAINT outbox_lock_pair CHECK ((locked_by IS NULL) = (locked_until IS NULL))
);

CREATE INDEX outbox_due_idx ON outbox_events (next_attempt_at) WHERE published_at IS NULL;
CREATE INDEX outbox_aggregate_idx ON outbox_events (aggregate_id, occurred_at);
