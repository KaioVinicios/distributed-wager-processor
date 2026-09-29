-- data-model.md §3.1
CREATE TABLE wallets (
    id            UUID        PRIMARY KEY,
    player_id     UUID        NOT NULL,
    currency      CHAR(3)     NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    balance_minor BIGINT      NOT NULL CHECK (balance_minor >= 0),          -- WAL-04
    version       BIGINT      NOT NULL CHECK (version >= 1),                -- WAL-07
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    CONSTRAINT wallets_player_currency_uq UNIQUE (player_id, currency),     -- WAL-03
    CONSTRAINT wallets_updated_after_created CHECK (updated_at >= created_at)
);
