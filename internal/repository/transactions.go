package repository

import (
	"context"
	"fmt"
	"strings"

	"keychain-wallet/internal/models"
)

// ListTransactions returns a wallet's ledger newest-first with keyset pagination
// and optional reference/type filters; the returned cursor is nil on the last page.
func (r *Repo) ListTransactions(ctx context.Context, walletID string, f models.TxFilter) ([]models.Entry, *models.Cursor, error) {
	// Distinguish "no wallet" (404) from "wallet with no entries" (empty 200).
	var exists bool
	if err := r.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM wallets WHERE id = $1::uuid)`, walletID).Scan(&exists); err != nil {
		return nil, nil, fmt.Errorf("list: check wallet: %w", err)
	}
	if !exists {
		return nil, nil, models.ErrWalletNotFound
	}

	args := []any{walletID}
	where := []string{"wallet_id = $1::uuid"}

	if f.Reference != "" {
		args = append(args, f.Reference)
		where = append(where, fmt.Sprintf("reference = $%d", len(args)))
	}
	if f.Type != "" {
		args = append(args, string(f.Type))
		where = append(where, fmt.Sprintf("type = $%d", len(args)))
	}
	if f.Cursor != nil {
		// Keyset: rows strictly "after" the cursor in (created_at DESC, id DESC) order.
		args = append(args, f.Cursor.CreatedAt, f.Cursor.ID)
		where = append(where, fmt.Sprintf("(created_at, id) < ($%d, $%d::uuid)", len(args)-1, len(args)))
	}

	args = append(args, f.Limit+1) // fetch one extra to detect a next page
	query := fmt.Sprintf(
		`SELECT id::text, wallet_id::text, type, amount_minor, balance_after, COALESCE(reference, ''), created_at
		 FROM ledger_entries
		 WHERE %s
		 ORDER BY created_at DESC, id DESC
		 LIMIT $%d`,
		strings.Join(where, " AND "), len(args))

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list: query: %w", err)
	}
	defer rows.Close()

	entries := make([]models.Entry, 0, f.Limit)
	for rows.Next() {
		var e models.Entry
		var t string
		if err := rows.Scan(&e.ID, &e.WalletID, &t, &e.AmountMinor, &e.BalanceAfter, &e.Reference, &e.CreatedAt); err != nil {
			return nil, nil, fmt.Errorf("list: scan: %w", err)
		}
		e.Type = models.TxType(t)
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("list: rows: %w", err)
	}

	var next *models.Cursor
	if len(entries) > f.Limit {
		last := entries[f.Limit-1]
		entries = entries[:f.Limit]
		next = &models.Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return entries, next, nil
}
