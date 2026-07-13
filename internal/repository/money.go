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

// Deduct debits amountMinor for an order. Idempotent on orderID; runs as one transaction.
func (r *Repo) Deduct(ctx context.Context, walletID, orderID string, amountMinor int64) (*models.MoneyResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("deduct: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	// Guard-first: a duplicate order conflicts before any money moves; the FK also catches a missing wallet.
	err = insertGuard(ctx, tx, models.TxDeduct, orderID, walletID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case pgerrcode.UniqueViolation:
				_ = tx.Rollback(ctx)
				return r.replay(ctx, orderID, walletID, models.TxDeduct)
			case pgerrcode.ForeignKeyViolation:
				return nil, models.ErrWalletNotFound
			}
		}
		return nil, fmt.Errorf("deduct: insert guard: %w", err)
	}

	// Atomic check-and-subtract; correct under concurrency at READ COMMITTED.
	// 0 rows => insufficient (the wallet is known to exist from the guard FK).
	var balanceAfter int64
	var currency string
	err = tx.QueryRow(ctx,
		`UPDATE wallets SET balance_minor = balance_minor - $1, updated_at = now()
		 WHERE id = $2::uuid AND balance_minor >= $1
		 RETURNING balance_minor, currency`,
		amountMinor, walletID).Scan(&balanceAfter, &currency)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Rollback leaves the guard unpersisted: a rejected deduct doesn't burn the key.
			var bal int64
			if e := tx.QueryRow(ctx, `SELECT balance_minor FROM wallets WHERE id = $1::uuid`, walletID).Scan(&bal); e != nil {
				return nil, fmt.Errorf("deduct: read balance: %w", e)
			}
			return nil, &models.InsufficientBalanceError{BalanceMinor: bal, RequiredMinor: amountMinor}
		}
		return nil, fmt.Errorf("deduct: update balance: %w", err)
	}

	entry, err := insertEntry(ctx, tx, walletID, models.TxDeduct, -amountMinor, balanceAfter, orderID)
	if err != nil {
		return nil, fmt.Errorf("deduct: insert ledger: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("deduct: commit: %w", err)
	}
	return &models.MoneyResult{Entry: entry, Currency: strings.TrimSpace(currency), Replayed: false}, nil
}

// TopUp credits amountMinor. Idempotent on paymentRef; unconditional (never insufficient).
func (r *Repo) TopUp(ctx context.Context, walletID, paymentRef string, amountMinor int64) (*models.MoneyResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("topup: begin: %w", err)
	}
	defer tx.Rollback(ctx)

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

	var balanceAfter int64
	var currency string
	err = tx.QueryRow(ctx,
		`UPDATE wallets SET balance_minor = balance_minor + $1, updated_at = now()
		 WHERE id = $2::uuid
		 RETURNING balance_minor, currency`,
		amountMinor, walletID).Scan(&balanceAfter, &currency)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, models.ErrWalletNotFound
		}
		return nil, fmt.Errorf("topup: update balance: %w", err)
	}

	entry, err := insertEntry(ctx, tx, walletID, models.TxTopUp, amountMinor, balanceAfter, paymentRef)
	if err != nil {
		return nil, fmt.Errorf("topup: insert ledger: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("topup: commit: %w", err)
	}
	return &models.MoneyResult{Entry: entry, Currency: strings.TrimSpace(currency), Replayed: false}, nil
}
