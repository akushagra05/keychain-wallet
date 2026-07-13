-- wallets: one account per (customer, currency) with a materialized balance (paise).
-- CHECK is the non-negative backstop; the UNIQUE makes creation idempotent (get-or-create).
CREATE TABLE wallets (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id   TEXT        NOT NULL,
    currency      CHAR(3)     NOT NULL DEFAULT 'INR',
    balance_minor BIGINT      NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT wallets_balance_non_negative CHECK (balance_minor >= 0),
    CONSTRAINT wallets_customer_currency_unique UNIQUE (customer_id, currency)
);

-- ledger_entries: append-only source of truth. amount_minor is signed (+credit / -debit);
-- the type<->sign CHECK makes an inconsistent row impossible.
CREATE TABLE ledger_entries (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    wallet_id     UUID        NOT NULL REFERENCES wallets(id),
    type          TEXT        NOT NULL,
    amount_minor  BIGINT      NOT NULL,
    balance_after BIGINT      NOT NULL,
    reference     TEXT,                            -- order_id (DEDUCT) / payment_ref (TOPUP)
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT ledger_amount_nonzero CHECK (amount_minor <> 0),
    CONSTRAINT ledger_balance_after_non_negative CHECK (balance_after >= 0),
    CONSTRAINT ledger_type_sign_consistent CHECK (
        (type = 'TOPUP'  AND amount_minor > 0) OR
        (type = 'DEDUCT' AND amount_minor < 0)
    )
);

-- newest-first history + keyset pagination
CREATE INDEX ledger_entries_wallet_created_idx ON ledger_entries (wallet_id, created_at DESC, id DESC);
-- replay + ?reference= lookups
CREATE INDEX ledger_entries_reference_idx ON ledger_entries (reference);

-- idempotency_keys: guard-first dedup; the composite PK namespaces the two keyspaces.
CREATE TABLE idempotency_keys (
    scope      TEXT        NOT NULL,   -- 'DEDUCT' | 'TOPUP'
    key        TEXT        NOT NULL,   -- order_id | payment_ref
    wallet_id  UUID        NOT NULL REFERENCES wallets(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key),
    CONSTRAINT idempotency_scope_valid CHECK (scope IN ('DEDUCT', 'TOPUP'))
);
