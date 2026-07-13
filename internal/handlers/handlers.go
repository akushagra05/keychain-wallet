// Package handlers is the HTTP layer: routing, DTOs, and error-to-status mapping.
package handlers

import (
	"log/slog"
	"net/http"
	"strconv"

	"keychain-wallet/internal/models"
	"keychain-wallet/internal/service"
)

type Handler struct {
	svc *service.Service
	log *slog.Logger
}

func New(svc *service.Service, log *slog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// POST /wallets
func (h *Handler) createWallet(w http.ResponseWriter, r *http.Request) {
	caller, err := callerID(r) // owner comes from the authenticated caller, not the body
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var req createWalletRequest
	if err := decodeJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	wallet, err := h.svc.CreateWallet(r.Context(), caller, req.Currency)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, wallet)
}

// POST /wallets/{id}/topup
func (h *Handler) topup(w http.ResponseWriter, r *http.Request) {
	walletID, err := pathUUID(r, "id")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if _, err := h.requireOwnership(r, walletID); err != nil {
		h.writeError(w, r, err)
		return
	}
	var req topupRequest
	if err := decodeJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	res, err := h.svc.TopUp(r.Context(), walletID, req.PaymentRef, req.AmountMinor)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if res.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusOK, newMoneyResponse(res))
}

// POST /wallets/{id}/deduct
func (h *Handler) deduct(w http.ResponseWriter, r *http.Request) {
	walletID, err := pathUUID(r, "id")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if _, err := h.requireOwnership(r, walletID); err != nil {
		h.writeError(w, r, err)
		return
	}
	var req deductRequest
	if err := decodeJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	res, err := h.svc.Deduct(r.Context(), walletID, req.OrderID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if res.Replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	writeJSON(w, http.StatusOK, newMoneyResponse(res))
}

// GET /wallets/{id}/balance
func (h *Handler) balance(w http.ResponseWriter, r *http.Request) {
	walletID, err := pathUUID(r, "id")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	wallet, err := h.svc.GetBalance(r.Context(), walletID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, balanceResponse{
		WalletID:     wallet.ID,
		BalanceMinor: wallet.BalanceMinor,
		Currency:     wallet.Currency,
	})
}

// GET /wallets/{id}/transactions
func (h *Handler) transactions(w http.ResponseWriter, r *http.Request) {
	walletID, err := pathUUID(r, "id")
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if _, err := h.requireOwnership(r, walletID); err != nil {
		h.writeError(w, r, err)
		return
	}
	q := r.URL.Query()

	limit := 0
	if raw := q.Get("limit"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil {
			h.writeError(w, r, models.Validationf("limit must be an integer"))
			return
		}
		limit = n
	}

	entries, next, err := h.svc.ListTransactions(r.Context(), walletID, limit, q.Get("cursor"), q.Get("reference"), q.Get("type"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	var nextToken *string
	if next != nil {
		t := next.Encode()
		nextToken = &t
	}
	writeJSON(w, http.StatusOK, transactionsResponse{
		Data:       entries,
		NextCursor: nextToken,
		Limit:      service.ClampPageLimit(limit),
	})
}
