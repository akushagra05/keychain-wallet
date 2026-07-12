// Package models holds the domain types and errors shared across layers.
// It has no dependency on the HTTP or persistence layers.
package models

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// TxType is the kind of money movement recorded on a ledger entry.
// It is plain data (a column value), not a behavioural dispatcher.
type TxType string

const (
	TxTopUp  TxType = "TOPUP"
	TxDeduct TxType = "DEDUCT"
)

// Wallet is an account holding a materialized balance in integer minor units (paise).
type Wallet struct {
	ID           string    `json:"id"`
	CustomerID   string    `json:"customer_id"`
	Currency     string    `json:"currency"`
	BalanceMinor int64     `json:"balance_minor"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Entry is one immutable ledger record. AmountMinor is signed (+credit / -debit).
// BalanceAfter is the wallet balance at the instant this entry committed.
type Entry struct {
	ID           string    `json:"id"`
	WalletID     string    `json:"wallet_id"`
	Type         TxType    `json:"type"`
	AmountMinor  int64     `json:"amount_minor"`
	BalanceAfter int64     `json:"balance_after"`
	Reference    string    `json:"reference"`
	CreatedAt    time.Time `json:"created_at"`
}

// MoneyResult is the outcome of a topup/deduct: the ledger entry, the wallet's
// currency, and whether this was an idempotent replay of an earlier request.
type MoneyResult struct {
	Entry    *Entry
	Currency string
	Replayed bool
}

// TxFilter parameterizes the transaction-history query.
type TxFilter struct {
	Limit     int
	Cursor    *Cursor // nil for the first page
	Reference string  // optional exact match on the reference column
	Type      TxType  // optional exact match on the entry type
}

// Cursor is an opaque keyset-pagination position over (created_at, id).
// Keyset (not OFFSET) keeps deep pages fast and stable under concurrent inserts.
type Cursor struct {
	CreatedAt time.Time
	ID        string
}

// Encode renders the cursor as an opaque base64 token for the client.
func (c Cursor) Encode() string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses a token produced by Encode.
func DecodeCursor(token string) (*Cursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor encoding")
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid cursor format")
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid cursor timestamp")
	}
	return &Cursor{CreatedAt: ts, ID: parts[1]}, nil
}
