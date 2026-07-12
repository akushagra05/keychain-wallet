package handlers

import "net/http"

// Routes builds the HTTP handler: the Go 1.22+ ServeMux (method + path patterns,
// no router dependency) wrapped in the middleware chain.
//
// Chain (outer → inner): requestID → logging → recoverPanic → mux.
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
