package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"keychain-wallet/internal/models"
)

// Deduct debits amountMinor from a wallet for an order. It is idempotent on
// orderID and safe under concurrency.
//
// The three steps run in ONE transaction, so every failure collapses to either
// "nothing committed" or "everything committed" — there is no partial state.
func (r *Repo) Deduct(ctx context.Context, walletID, orderID string, amountMinor int64) (*models.MoneyResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("deduct: begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op after a successful commit

	// 1. Guard-first. A duplicate order conflicts HERE, before any money moves,
	//    so a retry replays instead of double-charging. The FK on wallet_id also
	//    means a non-existent wallet fails here (23503) rather than later.
	err = insertGuard(ctx, tx, models.TxDeduct, orderID, walletID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case pgerrcode.UniqueViolation: // duplicate order -> replay the original result
				_ = tx.Rollback(ctx)
				return r.replay(ctx, orderID, walletID, models.TxDeduct)
			case pgerrcode.ForeignKeyViolation: // wallet does not exist
				return nil, models.ErrWalletNotFound
			}
		}
		return nil, fmt.Errorf("deduct: insert guard: %w", err)
	}

	// 2. Atomic conditional decrement: check-and-subtract in a single statement.
	//    Correct at READ COMMITTED because a concurrent deduct blocks on the row
	//    lock and re-evaluates `balance_minor >= amount` against the fresh value.
	//    0 rows => insufficient (the wallet is guaranteed to exist by step 1).
	var balanceAfter int64
	var currency string
	err = tx.QueryRow(ctx,
		`UPDATE wallets SET balance_minor = balance_minor - $1, updated_at = now()
		 WHERE id = $2::uuid AND balance_minor >= $1
		 RETURNING balance_minor, currency`,
		amountMinor, walletID).Scan(&balanceAfter, &currency)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var bal int64
			if e := tx.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id = $1::uuid`, walletID).Scan(&bal); e != nil {
				return nil, fmt.Errorf("deduct: read balance: %w", e)
			}
			// Rolling back means the guard row is NOT persisted: a failed deduct
			// does not burn the key, so a retry after a top-up can succeed.
			return nil, &models.InsufficientBalanceError{BalanceMinor: bal, RequiredMinor: amountMinor}
		}
		return nil, fmt.Errorf("deduct: update balance: %w", err)
	}

	// 3. Append the immutable ledger entry (single clean write; balance_after known).
	entry, err := insertEntry(ctx, tx, walletID, models.TxDeduct, -amountMinor, balanceAfter, orderID)
	if err != nil {
		return nil, fmt.Errorf("deduct: insert ledger: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("deduct: commit: %w", err)
	}
	return &models.MoneyResult{Entry: entry, Currency: strings.TrimSpace(currency), Replayed: false}, nil
}

// TopUp credits amountMinor to a wallet. It is idempotent on paymentRef.
// Adding money is unconditional (it can never be "insufficient").
func (r *Repo) TopUp(ctx context.Context, walletID, paymentRef string, amountMinor int64) (*models.MoneyResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("topup: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// 1. Guard-first (idempotent on payment_ref); FK gives wallet-existence.
	err = insertGuard(ctx, tx, models.TxTopUp, paymentRef, walletID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case pgerrcode.UniqueViolation:
				_ = tx.Rollback(ctx)
				return r.replay(ctx, paymentRef, walletID, models.TxTopUp)
			case pgerrcode.ForeignKeyViolation:
				return nil, models.ErrWalletNotFound
			}
		}
		return nil, fmt.Errorf("topup: insert guard: %w", err)
	}

	// 2. Unconditional credit. Concurrent writes serialize on the row lock.
	var balanceAfter int64
	var currency string
	err = tx.QueryRow(ctx,
		`UPDATE wallets SET balance_minor = balance_minor + $1, updated_at = now()
		 WHERE id = $2::uuid
		 RETURNING balance_minor, currency`,
		amountMinor, walletID).Scan(&balanceAfter, &currency)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) { // unreachable given the guard FK, but defensive
			return nil, models.ErrWalletNotFound
		}
		return nil, fmt.Errorf("topup: update balance: %w", err)
	}

	// 3. Append the ledger entry.
	entry, err := insertEntry(ctx, tx, walletID, models.TxTopUp, amountMinor, balanceAfter, paymentRef)
	if err != nil {
		return nil, fmt.Errorf("topup: insert ledger: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("topup: commit: %w", err)
	}
	return &models.MoneyResult{Entry: entry, Currency: strings.TrimSpace(currency), Replayed: false}, nil
}
