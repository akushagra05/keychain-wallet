package handlers

import "net/http"

// Routes builds the stdlib ServeMux (Go 1.22 method+path routing) wrapped in the
// middleware chain (outer→inner: requestID → logging → recoverPanic).
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /wallets", h.createWallet)
	mux.HandleFunc("POST /wallets/{id}/topup", h.topup)
	mux.HandleFunc("POST /wallets/{id}/deduct", h.deduct)
	mux.HandleFunc("GET /wallets/{id}/balance", h.balance)
	mux.HandleFunc("GET /wallets/{id}/transactions", h.transactions)
	mux.HandleFunc("GET /healthz", h.health)

	return requestID(h.logging(h.recoverPanic(mux)))
}
