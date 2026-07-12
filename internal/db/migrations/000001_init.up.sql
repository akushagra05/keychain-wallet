-- wallets: the account. balance_minor is a MATERIALIZED projection of the ledger,
-- kept consistent inside the same transaction as every money movement.
-- The CHECK is the hard, DB-enforced backstop for the core invariant: a wallet
-- can never go negative, even if application code regresses.
CREATE TABLE wallets (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id   TEXT        NOT NULL,          -- opaque reference owned by the upstream User service
    currency      CHAR(3)     NOT NULL DEFAULT 'INR',
    balance_minor BIGINT      NOT NULL DEFAULT 0, -- integer minor units (paise); never float
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT wallets_balance_non_negative CHECK (balance_minor >= 0)
);

-- ledger_entries: append-only, immutable source of truth for every money movement.
-- amount_minor is SIGNED (+credit / -debit) so SUM(amount_minor) = balance is trivial;
-- the type<->sign CHECK makes an inconsistent row impossible.
-- balance_after is the running-balance snapshot at the moment this entry committed
-- (self-auditing ledger; free "balance at any point in time").
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

-- Serves GET /transactions newest-first with keyset (cursor) pagination.
CREATE INDEX ledger_entries_wallet_created_idx
    ON ledger_entries (wallet_id, created_at DESC, id DESC);

-- Serves replay lookups and the ?reference= reconciliation filter.
CREATE INDEX ledger_entries_reference_idx
    ON ledger_entries (reference);

-- idempotency_keys: guard-first dedup, inserted BEFORE the balance mutation so a
-- duplicate conflicts before any money moves. scope namespaces the two keyspaces
-- (order_id vs payment_ref) so they can never collide.
CREATE TABLE idempotency_keys (
    scope      TEXT        NOT NULL,   -- 'DEDUCT' | 'TOPUP'
    key        TEXT        NOT NULL,   -- order_id (DEDUCT) | payment_ref (TOPUP)
    wallet_id  UUID        NOT NULL REFERENCES wallets(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key),
    CONSTRAINT idempotency_scope_valid CHECK (scope IN ('DEDUCT', 'TOPUP'))
);
