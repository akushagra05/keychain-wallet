// Package repository is the persistence layer: all SQL and transactions live here.
// It implements the interface the service layer defines and translates SQL
// outcomes (SQLSTATE codes, 0-row updates) into domain errors.
package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"keychain-wallet/internal/models"
)

// Repo is a Postgres-backed repository.
type Repo struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// querier is satisfied by both *pgxpool.Pool and pgx.Tx, so helpers work inside
// or outside a transaction.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// CreateWallet inserts a new wallet with a zero balance.
func (r *Repo) CreateWallet(ctx context.Context, customerID, currency string) (*models.Wallet, error) {
	row := r.pool.QueryRow(ctx,
		`INSERT INTO wallets (customer_id, currency) VALUES ($1, $2)
		 RETURNING id::text, customer_id, currency, balance_minor, created_at, updated_at`,
		customerID, currency)
	return scanWallet(row)
}

// GetWallet returns the wallet or models.ErrWalletNotFound.
func (r *Repo) GetWallet(ctx context.Context, walletID string) (*models.Wallet, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id::text, customer_id, currency, balance_minor, created_at, updated_at
		 FROM wallets WHERE id = $1::uuid`, walletID)
	w, err := scanWallet(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, models.ErrWalletNotFound
	}
	return w, err
}

// replay reconstructs the response for a duplicate idempotency key. It runs
// outside the (now rolled-back) write transaction, on the pool. A 23505 means
// the original transaction already COMMITTED, so its guard row and ledger entry
// are both visible here.
func (r *Repo) replay(ctx context.Context, key, walletID string, t models.TxType) (*models.MoneyResult, error) {
	var guardWallet string
	err := r.pool.QueryRow(ctx,
		`SELECT wallet_id::text FROM idempotency_keys WHERE scope = $1 AND key = $2`,
		string(t), key).Scan(&guardWallet)
	if err != nil {
		return nil, fmt.Errorf("replay: read guard: %w", err)
	}
	// Same key seen on a different wallet -> surface it, don't silently replay.
	if guardWallet != walletID {
		return nil, models.ErrIdempotencyConflict
	}
	entry, err := entryByReference(ctx, r.pool, walletID, t, key)
	if err != nil {
		return nil, fmt.Errorf("replay: read entry: %w", err)
	}
	var currency string
	if err := r.pool.QueryRow(ctx,
		`SELECT currency FROM wallets WHERE id = $1::uuid`, walletID).Scan(&currency); err != nil {
		return nil, fmt.Errorf("replay: read currency: %w", err)
	}
	return &models.MoneyResult{Entry: entry, Currency: strings.TrimSpace(currency), Replayed: true}, nil
}

// insertGuard writes the idempotency guard row. The scope is derived from the
// transaction type (they are 1:1 in this domain), so there are no magic strings.
// It returns the raw error so the caller can distinguish unique/FK violations.
func insertGuard(ctx context.Context, q querier, t models.TxType, key, walletID string) error {
	_, err := q.Exec(ctx,
		`INSERT INTO idempotency_keys (scope, key, wallet_id) VALUES ($1, $2, $3::uuid)`,
		string(t), key, walletID)
	return err
}

// insertEntry appends one immutable ledger row and returns it.
func insertEntry(ctx context.Context, q querier, walletID string, t models.TxType, amountMinor, balanceAfter int64, reference string) (*models.Entry, error) {
	row := q.QueryRow(ctx,
		`INSERT INTO ledger_entries (wallet_id, type, amount_minor, balance_after, reference)
		 VALUES ($1::uuid, $2, $3, $4, $5)
		 RETURNING id::text, wallet_id::text, type, amount_minor, balance_after, COALESCE(reference, ''), created_at`,
		walletID, string(t), amountMinor, balanceAfter, reference)
	return scanEntry(row)
}

// entryByReference fetches the single entry that a given (wallet, type, reference)
// maps to. Uniqueness is guaranteed by the idempotency guard.
func entryByReference(ctx context.Context, q querier, walletID string, t models.TxType, reference string) (*models.Entry, error) {
	row := q.QueryRow(ctx,
		`SELECT id::text, wallet_id::text, type, amount_minor, balance_after, COALESCE(reference, ''), created_at
		 FROM ledger_entries
		 WHERE wallet_id = $1::uuid AND type = $2 AND reference = $3
		 ORDER BY created_at DESC, id DESC
		 LIMIT 1`,
		walletID, string(t), reference)
	return scanEntry(row)
}

func scanWallet(row pgx.Row) (*models.Wallet, error) {
	var w models.Wallet
	if err := row.Scan(&w.ID, &w.CustomerID, &w.Currency, &w.BalanceMinor, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	w.Currency = strings.TrimSpace(w.Currency) // CHAR(3) safety
	return &w, nil
}

func scanEntry(row pgx.Row) (*models.Entry, error) {
	var e models.Entry
	var t string
	if err := row.Scan(&e.ID, &e.WalletID, &t, &e.AmountMinor, &e.BalanceAfter, &e.Reference, &e.CreatedAt); err != nil {
		return nil, err
	}
	e.Type = models.TxType(t)
	return &e, nil
}
