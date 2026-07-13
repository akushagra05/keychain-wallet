package models

import (
	"errors"
	"fmt"
)

// Sentinel domain errors, mapped to HTTP status by the handler layer.
var (
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrIdempotencyConflict = errors.New("idempotency key already used for a different wallet")
	ErrUnauthenticated     = errors.New("missing caller identity")
	ErrForbidden           = errors.New("wallet does not belong to caller")
)

// ValidationError is a client-input problem (400); Message is safe to return.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func Validationf(format string, a ...any) *ValidationError {
	return &ValidationError{Message: fmt.Sprintf(format, a...)}
}

// InsufficientBalanceError is the balance-constraint rejection (402), carrying the
// current balance and the required amount.
type InsufficientBalanceError struct {
	BalanceMinor  int64
	RequiredMinor int64
}

func (e *InsufficientBalanceError) Error() string {
	return fmt.Sprintf("insufficient balance: have %d, need %d", e.BalanceMinor, e.RequiredMinor)
}
