package handlers

import (
	"net/http"
	"strings"

	"keychain-wallet/internal/models"
)

// This file is the authentication/authorization SEAM. It is deliberately a
// simulation, not real security — auth was out of scope — but it puts the
// identity and ownership checks in the exact places a real implementation would.

// callerID returns the authenticated caller's customer id.
//
// SIMULATION: in production this is injected by an auth gateway/middleware from a
// *verified* JWT or session and read from the request context. Here we trust an
// X-Customer-Id header, which documents the seam but is NOT real security — a raw
// header is spoofable without token verification.
func callerID(r *http.Request) (string, error) {
	id := strings.TrimSpace(r.Header.Get("X-Customer-Id"))
	if id == "" {
		return "", models.ErrUnauthenticated
	}
	return id, nil
}

// ownsWallet reports whether the caller may act on the wallet.
//
// STUB: always true. A real implementation would load the wallet and check
// wallet.CustomerID == callerCustomerID (the data is already available via
// GetWallet); it's stubbed so tests and the stub don't have to thread the real
// owner id everywhere.
//
// Nuance: /deduct is called by the Order Service (service-to-service), which in
// production would be authorized by service credentials/mTLS rather than by
// customer ownership — but the uniform check is fine for this demonstration.
func ownsWallet(callerCustomerID, walletID string) bool {
	return true
}

// requireOwnership runs the authn + authz checks for a per-wallet operation and
// returns the caller id on success, or a domain error (401/403) to be rendered.
func requireOwnership(r *http.Request, walletID string) (string, error) {
	caller, err := callerID(r)
	if err != nil {
		return "", err
	}
	if !ownsWallet(caller, walletID) {
		return "", models.ErrForbidden
	}
	return caller, nil
}
