// Package service is the domain layer: business rules, input validation, and the
// deduct-amount policy. It depends on the Repository interface it defines here
// (persistence-agnostic) and knows nothing about HTTP or SQL.
package service

import (
	"context"
	"strings"

	"keychain-wallet/internal/models"
)

const (
	defaultCurrency  = "INR"
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// Repository is the persistence contract, defined consumer-side and satisfied by
// the repository package.
type Repository interface {
	CreateWallet(ctx context.Context, customerID, currency string) (*models.Wallet, error)
	GetWallet(ctx context.Context, walletID string) (*models.Wallet, error)
	TopUp(ctx context.Context, walletID, paymentRef string, amountMinor int64) (*models.MoneyResult, error)
	Deduct(ctx context.Context, walletID, orderID string, amountMinor int64) (*models.MoneyResult, error)
	ListTransactions(ctx context.Context, walletID string, f models.TxFilter) ([]models.Entry, *models.Cursor, error)
}

type Service struct {
	repo              Repository
	deductAmountMinor int64
}

func New(repo Repository, deductAmountMinor int64) *Service {
	return &Service{repo: repo, deductAmountMinor: deductAmountMinor}
}

// DeductAmountMinor exposes the configured per-order deduction (for the stub/tests).
func (s *Service) DeductAmountMinor() int64 { return s.deductAmountMinor }

// ClampPageLimit applies the default/max page-size bounds. Shared by the service
// and the handler so the response's reported limit matches what was applied.
func ClampPageLimit(n int) int {
	switch {
	case n <= 0:
		return defaultPageLimit
	case n > maxPageLimit:
		return maxPageLimit
	default:
		return n
	}
}

func (s *Service) CreateWallet(ctx context.Context, customerID, currency string) (*models.Wallet, error) {
	customerID = strings.TrimSpace(customerID)
	if customerID == "" {
		return nil, models.Validationf("customer_id is required")
	}
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "" {
		currency = defaultCurrency
	}
	if len(currency) != 3 {
		return nil, models.Validationf("currency must be a 3-letter ISO code")
	}
	return s.repo.CreateWallet(ctx, customerID, currency)
}

func (s *Service) GetBalance(ctx context.Context, walletID string) (*models.Wallet, error) {
	return s.repo.GetWallet(ctx, walletID)
}

func (s *Service) TopUp(ctx context.Context, walletID, paymentRef string, amountMinor int64) (*models.MoneyResult, error) {
	paymentRef = strings.TrimSpace(paymentRef)
	if paymentRef == "" {
		return nil, models.Validationf("payment_ref is required")
	}
	if amountMinor <= 0 {
		return nil, models.Validationf("amount_minor must be a positive integer")
	}
	return s.repo.TopUp(ctx, walletID, paymentRef, amountMinor)
}

// Deduct applies the fixed, server-side per-order amount (the spec's ₹100).
// Making the amount a config policy — not a client input — is deliberate: it
// keeps the idempotency contract simple (a retry can never carry a different amount).
func (s *Service) Deduct(ctx context.Context, walletID, orderID string) (*models.MoneyResult, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return nil, models.Validationf("order_id is required")
	}
	return s.repo.Deduct(ctx, walletID, orderID, s.deductAmountMinor)
}

// ListTransactions clamps the page size, decodes the cursor, and validates the
// optional type filter before delegating.
func (s *Service) ListTransactions(ctx context.Context, walletID string, limit int, cursorToken, reference, txType string) ([]models.Entry, *models.Cursor, error) {
	f := models.TxFilter{Limit: ClampPageLimit(limit), Reference: strings.TrimSpace(reference)}

	if cursorToken != "" {
		c, err := models.DecodeCursor(cursorToken)
		if err != nil {
			return nil, nil, models.Validationf("invalid cursor")
		}
		f.Cursor = c
	}

	if txType != "" {
		t := models.TxType(strings.ToUpper(strings.TrimSpace(txType)))
		if t != models.TxTopUp && t != models.TxDeduct {
			return nil, nil, models.Validationf("type must be TOPUP or DEDUCT")
		}
		f.Type = t
	}

	return s.repo.ListTransactions(ctx, walletID, f)
}
