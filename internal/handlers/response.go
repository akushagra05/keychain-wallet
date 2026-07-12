package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/google/uuid"

	"keychain-wallet/internal/models"
)

// ---- request DTOs ----

type createWalletRequest struct {
	// customer_id is intentionally NOT here — the owner comes from the
	// authenticated caller (X-Customer-Id), never from the request body.
	Currency string `json:"currency"`
}

type topupRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	PaymentRef  string `json:"payment_ref"`
}

type deductRequest struct {
	OrderID string `json:"order_id"`
}

// ---- response DTOs ----

type moneyResponse struct {
	WalletID     string        `json:"wallet_id"`
	BalanceMinor int64         `json:"balance_minor"`
	Currency     string        `json:"currency"`
	Entry        *models.Entry `json:"entry"`
}

func newMoneyResponse(res *models.MoneyResult) moneyResponse {
	return moneyResponse{
		WalletID:     res.Entry.WalletID,
		BalanceMinor: res.Entry.BalanceAfter,
		Currency:     res.Currency,
		Entry:        res.Entry,
	}
}

type balanceResponse struct {
	WalletID     string `json:"wallet_id"`
	BalanceMinor int64  `json:"balance_minor"`
	Currency     string `json:"currency"`
}

type transactionsResponse struct {
	Data       []models.Entry `json:"data"`
	NextCursor *string        `json:"next_cursor"`
	Limit      int            `json:"limit"`
}

// ---- error envelope ----

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: msg, Details: details}})
}

// writeError maps a domain error to the right HTTP status + machine-readable code.
// The code is what a caller (the Order Service) branches on.
func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *models.ValidationError
	var ibe *models.InsufficientBalanceError

	switch {
	case errors.As(err, &ve):
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", ve.Message, nil)
	case errors.Is(err, models.ErrWalletNotFound):
		writeErr(w, http.StatusNotFound, "WALLET_NOT_FOUND", "wallet not found", nil)
	case errors.As(err, &ibe):
		writeErr(w, http.StatusPaymentRequired, "INSUFFICIENT_BALANCE", ibe.Error(),
			map[string]any{"balance_minor": ibe.BalanceMinor, "required_minor": ibe.RequiredMinor})
	case errors.Is(err, models.ErrIdempotencyConflict):
		writeErr(w, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT",
			"idempotency key already used for a different wallet", nil)
	case errors.Is(err, models.ErrUnauthenticated):
		writeErr(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", nil)
	case errors.Is(err, models.ErrForbidden):
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "wallet does not belong to caller", nil)
	default:
		// Unexpected: log the real error, return a generic message. The request id
		// (in the X-Request-Id header) ties the client's 500 to the server log.
		h.log.ErrorContext(r.Context(), "internal error", "err", err, "path", r.URL.Path)
		writeErr(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal error", nil)
	}
}

// decodeJSON reads a JSON body (size-limited, unknown fields rejected). An empty
// body is allowed and leaves dst at its zero value — required-field checks happen
// downstream in the service (so e.g. POST /wallets with no body defaults currency).
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	err := dec.Decode(dst)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return models.Validationf("invalid JSON body: %v", err)
	}
	return nil
}

// pathUUID extracts and validates a UUID path parameter.
func pathUUID(r *http.Request, name string) (string, error) {
	raw := r.PathValue(name)
	if _, err := uuid.Parse(raw); err != nil {
		return "", models.Validationf("invalid wallet id")
	}
	return raw, nil
}
