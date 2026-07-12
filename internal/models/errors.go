package models

import (
	"errors"
	"fmt"
)

// Sentinel domain errors. Handlers map these to HTTP status + error codes;
// the persistence layer translates SQL outcomes into them.
var (
	// ErrWalletNotFound: the wallet id does not exist.
	ErrWalletNotFound = errors.New("wallet not found")

	// ErrIdempotencyConflict: the idempotency key was already used for a
	// DIFFERENT wallet (a serious upstream bug, surfaced rather than silently replayed).
	ErrIdempotencyConflict = errors.New("idempotency key already used for a different wallet")

	// ErrUnauthenticated: no caller identity was provided (401).
	ErrUnauthenticated = errors.New("missing caller identity")

	// ErrForbidden: the caller is not allowed to act on this wallet (403).
	ErrForbidden = errors.New("wallet does not belong to caller")
)

// ValidationError is a client-input problem (400). Message is safe to return.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// Validationf is a small constructor for ValidationError.
func Validationf(format string, a ...any) *ValidationError {
	return &ValidationError{Message: fmt.Sprintf(format, a...)}
}

// InsufficientBalanceError is the balance-constraint rejection (402). It carries
// the current balance and the required amount so the caller gets actionable detail.
type InsufficientBalanceError struct {
	BalanceMinor  int64
	RequiredMinor int64
}

func (e *InsufficientBalanceError) Error() string {
	return fmt.Sprintf("insufficient balance: have %d, need %d", e.BalanceMinor, e.RequiredMinor)
}
