# Keychain OS — Prepaid Wallet Service

A backend HTTP service that owns customer wallet balances, records every money
movement in an immutable ledger, and enforces the core invariant: **a wallet can
never go negative, and an order is charged at most once.**

Built in Go + PostgreSQL. The interesting part of this problem is not the five
endpoints — it's holding two correctness properties under concurrency and
network failure:

1. **The balance constraint** — deductions must never overdraw a wallet, even when many orders hit the same wallet simultaneously.
2. **Idempotency** — the Order Service retries over an unreliable network, and a retry must never double-charge.

Everything below is in service of those two.

---

## Quickstart

### Run the whole thing (Docker)

```bash
docker compose up --build          # starts Postgres + the wallet service on :8080
```

The service applies its own migrations on boot, so this is all it takes.

### Run locally

```bash
make db          # start only Postgres (docker compose)
make run         # go run ./cmd/walletd   (reads DATABASE_URL; see .env.example)
```

### Demo the Order Service integration

With the service running:

```bash
make stub        # go run ./cmd/orderstub
```

The stub creates a wallet, tops it up, places orders until funds run out (a `402`),
retries an already-charged order (idempotent replay — no double charge), retries a
top-up (no double credit), and prints the ledger.

### Tests

```bash
make test        # spins up a throwaway Postgres via testcontainers (needs Docker)
make test-race   # same, with the race detector
```

### Tooling

- **API collection:** open the [`bruno/`](bruno) folder in [Bruno](https://usebruno.com) → select the **Local** environment. Run **Create Wallet** first (it captures the new id into `{{walletId}}` for the rest).
- **Debugging:** [`.vscode/launch.json`](.vscode/launch.json) has one-click configs for the service, the stub, and the tests (VS Code Go extension).

---

## API

Money is always **integer minor units (paise)** plus a `currency` — never a float.
`₹100 = 10000`.

| Method & path | Auth | Body | Success | Notable errors |
|---|---|---|---|---|
| `POST /wallets` | `X-Customer-Id` | `{"currency?"}` | `201` wallet | `400` `401` |
| `POST /wallets/{id}/topup` | `X-Customer-Id` + owns | `{"amount_minor","payment_ref"}` | `200` `{wallet_id,balance_minor,currency,entry}` | `400` `401` `403` `404` `409` |
| `POST /wallets/{id}/deduct` | `X-Customer-Id` + owns | `{"order_id"}` | `200` `{wallet_id,balance_minor,currency,entry}` | `400` `401` `403` **`402`** `404` `409` |
| `GET /wallets/{id}/balance` | open | — | `200` `{wallet_id,balance_minor,currency}` | `404` |
| `GET /wallets/{id}/transactions` | `X-Customer-Id` + owns | `?limit&cursor&reference&type` | `200` `{data,next_cursor,limit}` | `400` `401` `403` `404` |
| `GET /healthz` | open | — | `200` | — |

- **Auth — real authorization, simulated identity.** The caller identity comes from an `X-Customer-Id` header (standing in for what an auth gateway would inject from a *verified* JWT/session) — so identity is a documented seam, **not** real security, since a raw header is spoofable. The **authorization is real**, though: per-wallet operations load the wallet and require `wallet.customer_id == caller` (403 otherwise). Wallet **owner** comes from the identity, never the request body. `balance` is left open (spec: "anyone"). See [internal/handlers/auth.go](internal/handlers/auth.go) + `AuthorizeWalletAccess`.
  - Nuance: `/deduct` is called by the Order Service (service-to-service), which in production would be authorized by service credentials/mTLS rather than customer ownership.
- The **deduct amount is fixed server-side** (`DEDUCT_AMOUNT_MINOR`, default ₹100). The deduct body carries only `order_id`.
- **Idempotency** is keyed on `order_id` (deduct) and `payment_ref` (topup). A retried request returns the *original* result with header `Idempotency-Replayed: true`.
- **Errors** use a consistent envelope; the `code` is what a caller branches on:
  ```json
  { "error": { "code": "INSUFFICIENT_BALANCE", "message": "...",
               "details": { "balance_minor": 0, "required_minor": 10000 } } }
  ```
  Codes: `VALIDATION_ERROR` (400), `UNAUTHENTICATED` (401), `INSUFFICIENT_BALANCE` (402), `FORBIDDEN` (403), `WALLET_NOT_FOUND` (404), `IDEMPOTENCY_KEY_CONFLICT` (409), `INTERNAL_ERROR` (500).
- Every response carries an `X-Request-Id` (echoed into logs) for tracing.
- `GET /transactions` uses **keyset (cursor) pagination**; `?reference=&type=` filters support reconciliation lookups ("was order X charged?").

---

## Data model

Three tables ([`internal/db/migrations`](internal/db/migrations)):

**`wallets`** — the account. `balance_minor` is a *materialized* balance with a
DB-level `CHECK (balance_minor >= 0)`.

**`ledger_entries`** — append-only, immutable source of truth. `amount_minor` is
**signed** (+credit / −debit); a `CHECK` ties `type` to sign so an inconsistent
row is impossible. `balance_after` snapshots the running balance at each entry.

**`idempotency_keys`** — the dedup guard. Composite PK `(scope, key)` where
`scope ∈ {DEDUCT, TOPUP}` namespaces the two keyspaces (`order_id` vs `payment_ref`)
so they can never collide.

---

## Key design decisions

### The ledger is the source of truth; the balance is a materialized projection
Balance could be *derived* (`SUM(ledger)`) or *stored*. I keep both: an immutable
append-only ledger (full audit trail, self-verifying via `balance_after`) **and** a
materialized `balance_minor` column kept consistent inside the same transaction.
This gives O(1) balance reads *and* a ledger that can recompute/verify the balance.
The materialized column is effectively a cache — but a transactionally-consistent
one that can't go stale, which is why the service needs **no external cache**.

### Money is integer paise, never float
Floats can't represent money exactly. Integer minor units are exact by construction,
fast, the payments-industry convention (Stripe et al.), and map cleanly to Go `int64`
with no decimal library or `NUMERIC`-as-string marshaling. `DECIMAL(x,2)` would also
be correct; I preferred integers for the Go ergonomics and to avoid a scale to configure.

### The balance constraint: one atomic conditional statement
Deduct is check-and-subtract in a **single statement**:

```sql
UPDATE wallets SET balance_minor = balance_minor - $amt
WHERE id = $id AND balance_minor >= $amt
RETURNING balance_minor;
```

`0 rows affected` **is** the "insufficient balance" signal. There is no
read-then-write window, so there is no race. This is correct at Postgres's default
**READ COMMITTED** isolation: a concurrent deduct blocks on the row lock and then
re-evaluates `balance_minor >= $amt` against the freshly-committed value, so the
loser matches 0 rows. No `SELECT … FOR UPDATE`, no `SERIALIZABLE`, no retry loop.

A DB `CHECK (balance_minor >= 0)` is the **backstop** — even a buggy `UPDATE` that
bypassed all application logic cannot drive a wallet negative (there's a test that
asserts exactly this).

**Concurrency is enforced by the database, never the application** — no Go mutexes.
That's what keeps it correct when you run *N* instances of the service horizontally.

### Idempotency: guard-first, exactly-once-on-success
The idempotency key is inserted **before** any money moves. This ordering is the
whole game:

```
1. INSERT idempotency guard         -- duplicate? -> unique violation -> replay original
2. UPDATE balance (conditional)     -- 0 rows -> INSUFFICIENT (wallet exists, by step-1 FK)
3. INSERT ledger entry              -- single clean append; balance_after known
   COMMIT
```

Because the guard, the balance update, and the ledger insert share **one
transaction**, several properties fall out for free:

- **The key is committed iff the deduction succeeded.** A deduct rejected for
  insufficient balance rolls back and does *not* burn the key — so a retry *after a
  top-up* can legitimately succeed.
- **Concurrent duplicates are safe.** The second identical request blocks on the
  pending unique key; when the first commits, the second gets a unique violation and
  **replays** the original result. No explicit `PENDING/COMPLETED` state machine needed.
- **A committed guard always has a committed ledger entry** (they share the tx), so
  there are never orphaned keys.
- **The FK on `wallet_id` gives wallet-existence for free** at step 1, which is why
  `0 rows` at step 2 unambiguously means "insufficient," not "wallet missing."

Guard-first matters because the alternative — checking the balance first — makes
`0 rows` *ambiguous*: it could mean "genuinely broke" *or* "a duplicate whose balance
was already spent by the original." Those need opposite responses (reject vs. replay);
guard-first removes the ambiguity by asking "have I already done this?" before
"is there money?".

The guard lives in a **separate table** rather than a unique column on the ledger so
the ledger stays a pure single-write append (guard-first + `balance_after` from
`RETURNING` don't fight each other). Topup uses the identical mechanism keyed on
`payment_ref`, so a retried payment webhook can't double-credit.

### Failure modes collapse to two outcomes
Because every mutation is one transaction, any failure lands in exactly one of two
states: **nothing committed** (retry applies fresh) or **everything committed** (retry
replays). There is no partial state — no "money moved but no ledger row." A timed-out
caller's only safe action is to retry with the same key, which is always correct. The
stub demonstrates the caller-side retry classification: retry transient failures
(timeout / 5xx), treat `402`/`4xx` as final.

### Explicit SQL over an ORM
The correctness-critical logic *is* the SQL (atomic conditional update, guard-first
ordering, `RETURNING`). An ORM's idiomatic load-modify-save is a read-modify-write —
the exact race this design eliminates — and its error mapping hides the SQLSTATE
(`23505`) the replay path depends on. So the persistence layer is hand-written pgx.
`sqlc` would be a reasonable "typed queries without an ORM" step up; a full ORM (GORM)
would be the wrong tool for a correctness-critical ledger.

### Architecture
`handlers → service → repository`, with domain types/errors in `models`. The service
defines the `Repository` interface it needs (persistence-agnostic); the repository
implements it and owns all SQL/transactions. The transaction boundary lives entirely
in the persistence layer via coarse methods (`Deduct`, `TopUp`), so the domain never
sees a transaction.

```
cmd/walletd      – service entrypoint (config, pool, auto-migrate, serve)
cmd/orderstub    – Order Service stub / live demo
internal/handlers    – HTTP: routing (stdlib 1.22 mux), DTOs, error mapping, middleware
internal/service     – domain: validation, deduct-amount policy, Repository interface
internal/repository  – persistence: pgx, guard-first transactions
internal/models      – domain types, typed errors, cursor
internal/db          – embedded migrations + runner
```

---

## Testing methodology

A financial system's correctness is about **invariants under concurrency**, so the
tests are built around that rather than the happy path. They run against a **real
Postgres** (spun up per run via testcontainers) driven through the full HTTP stack —
concurrency and transactional behaviour cannot be faithfully mocked.

The tests that matter ([`internal/integration`](internal/integration)):

- **`TestDeduct_Concurrency`** (the centerpiece) — 25 distinct orders race for a
  wallet funded for 5. Asserts *exactly* 5 succeed, the rest get `402`, the balance
  lands at 0 (never negative), and the ledger has exactly 5 debit rows.
- **`TestDeduct_Idempotent_Concurrent`** — 20 goroutines fire the *same* order at
  once; asserts exactly one deduction and that every response reports the same balance.
- **`TestDeduct_InsufficientBalance_KeyNotBurned`** — a rejected deduct writes no
  ledger row and does not burn the key; a retry after top-up succeeds.
- **`TestDeduct_Boundary`** — balance exactly ₹100: one deduct succeeds, the next `402`s.
- **`TestInvariant_BalanceEqualsLedgerSum`** — after a mixed workload, `balance == SUM(ledger)`.
- **`TestCheckConstraint_BlocksNegativeBalance`** — a raw `UPDATE` to a negative
  balance is rejected by the DB (the backstop).
- Plus idempotency (sequential + topup), cross-wallet key conflict (`409`),
  not-found (`404`), validation (`400`), keyset pagination, and the auth seam
  (`TestAuth_MissingIdentity`: `401` without identity, balance open;
  `TestAuth_WrongOwner`: `403` for a non-owner).

All pass under `-race`.

---

## Known limitations (honest)

- **Ledger ordering under heavy concurrency.** `created_at` comes from `now()`
  (transaction-start time), which can differ slightly from commit order. Each
  `balance_after` is individually correct, but ordering history by `created_at` and
  reading the column down the page could look non-monotonic in rare overlapping cases.
  Strict apply-order sequencing needs a dedicated post-commit sequence — deliberately
  out of scope here.
- **Wallet creation is not idempotent.** A lost-response retry of `POST /wallets` can
  create a duplicate wallet. Low stakes (it's setup); the fix is a client-supplied
  creation key.
- **Auth identity is simulated (spoofable).** The *authorization* is real (per-wallet
  ownership: `wallet.customer_id == caller`, with a 403 test), but it's only as strong
  as the identity it checks — and identity is trusted from an `X-Customer-Id` header,
  not a verified JWT. A real system verifies a JWT/session; `/deduct`, being
  service-to-service, would use service credentials. No rate limiting either.

---

## What I'd do with more time

- **Holds / auth-capture** — reserve funds on order placement, capture on ship or
  release on cancel. Additive: a `held_minor` column (`available = balance − held`), a
  `holds` table, and `HOLD/CAPTURE/RELEASE` entry types. Every primitive here carries over.
- **Double-entry accounting** — today's single-entry ledger is the customer-wallet
  *leg* of a double-entry system. Generalize `wallet_id → account_id`, add platform
  accounts, and make each movement a balanced set of postings that sum to zero.
- **Transactional outbox** — emit domain events on every money movement (for
  notifications/analytics) by writing to an `outbox` table in the same transaction and
  relaying with at-least-once delivery. The correct, crash-safe way to notify downstream
  — never an in-process callback in the write path.
- **Reconciliation job** — periodically assert `balance == SUM(ledger)` per wallet and
  alert on drift; heal the materialized balance from the ledger if they ever disagree.
- **Scaling reads** — read replicas for balance/transaction reads (not a cache) if read
  QPS outgrows the primary. Shard the idempotency/ledger data by key hash for global
  uniqueness if the service is ever partitioned.
- **Ops** — idempotency-key TTL/pruning, richer health/readiness checks, metrics
  (deduct success/reject rates), and revoking `UPDATE/DELETE` on the ledger to enforce
  immutability at the grant level.
