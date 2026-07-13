package handlers

import (
	"net/http"
	"strings"

	"keychain-wallet/internal/models"
)

// callerID returns the caller's customer id from X-Customer-Id.
// SIMULATION: real auth would set this from a verified JWT; a raw header is spoofable.
func callerID(r *http.Request) (string, error) {
	id := strings.TrimSpace(r.Header.Get("X-Customer-Id"))
	if id == "" {
		return "", models.ErrUnauthenticated
	}
	return id, nil
}

// requireOwnership checks identity (401) and wallet ownership (403; 404 if the wallet
// is missing). Note: /deduct is really service-to-service and would use service creds.
func (h *Handler) requireOwnership(r *http.Request, walletID string) (string, error) {
	caller, err := callerID(r)
	if err != nil {
		return "", err
	}
	if err := h.svc.AuthorizeWalletAccess(r.Context(), caller, walletID); err != nil {
		return "", err
	}
	return caller, nil
}
