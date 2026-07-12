// Command orderstub is a stand-in for the Order Service. It drives the Wallet
// Service over HTTP to demonstrate the /deduct integration end to end:
//
//  1. create a wallet and top it up
//  2. place orders (each deducts ₹100) until funds run out (402)
//  3. retry an already-charged order -> idempotent replay (no double charge)
//  4. retry the top-up -> idempotent replay (no double credit)
//  5. print the resulting ledger
//
// It also shows the retry-classification a real caller would use: retry only
// transient failures, treat 402/4xx as final.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// demoCustomer stands in for the authenticated caller identity that a real
// Order Service would present. It's sent as X-Customer-Id on every request.
const demoCustomer = "cust_demo"

func baseURL() string {
	if v := os.Getenv("WALLET_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

func main() {
	fmt.Println("== Order Service stub -> Wallet Service ==")

	// 1. Create a wallet and fund it with ₹500 (5 orders' worth).
	wallet := mustPost("/wallets", map[string]any{}) // owner comes from X-Customer-Id
	walletID := wallet["id"].(string)
	fmt.Printf("\n[setup] created wallet %s\n", walletID)

	topup(walletID, "pay_demo_001", 50000) // ₹500
	fmt.Printf("[setup] topped up ₹500 -> balance ₹%s\n", rupees(balance(walletID)))

	// 2. Place 6 orders; the wallet only funds 5, so #6 is rejected.
	fmt.Println("\n[orders] placing 6 orders (₹100 each) against a ₹500 wallet:")
	for i := 1; i <= 6; i++ {
		orderID := fmt.Sprintf("order_%03d", i)
		status, body, _ := deduct(walletID, orderID)
		reportOrder(orderID, status, body)
	}

	// 3. Idempotency: retry order_001 (already charged). Must replay, not re-charge.
	fmt.Println("\n[idempotency] retrying order_001 (already charged):")
	before := balance(walletID)
	status, body, replayed := deduct(walletID, "order_001")
	fmt.Printf("  status=%d replayed=%v  balance ₹%s -> ₹%s (unchanged = correct)\n",
		status, replayed, rupees(before), rupees(balance(walletID)))
	_ = body

	// 4. Top-up idempotency: retry the same payment_ref. Must replay, not re-credit.
	fmt.Println("\n[idempotency] retrying top-up pay_demo_001:")
	before = balance(walletID)
	topup(walletID, "pay_demo_001", 50000)
	fmt.Printf("  balance ₹%s -> ₹%s (unchanged = correct)\n", rupees(before), rupees(balance(walletID)))

	// 5. Print the ledger.
	fmt.Println("\n[ledger] transaction history:")
	txns := mustGet(fmt.Sprintf("/wallets/%s/transactions", walletID))
	for _, e := range txns["data"].([]any) {
		m := e.(map[string]any)
		fmt.Printf("  %-6s %8s  balance_after=₹%-6s ref=%s\n",
			m["type"], signedRupees(m["amount_minor"].(float64)), rupees(int64(m["balance_after"].(float64))), m["reference"])
	}

	fmt.Println("\n== done ==")
}

// ---- domain operations ----

func topup(walletID, paymentRef string, amountMinor int64) {
	status, body, _ := doJSON(http.MethodPost, fmt.Sprintf("/wallets/%s/topup", walletID),
		map[string]any{"amount_minor": amountMinor, "payment_ref": paymentRef})
	if status != http.StatusOK {
		fatalf("topup failed: status=%d body=%v", status, body)
	}
}

func deduct(walletID, orderID string) (int, map[string]any, bool) {
	status, body, hdr := doJSON(http.MethodPost, fmt.Sprintf("/wallets/%s/deduct", walletID),
		map[string]any{"order_id": orderID})
	return status, body, hdr.Get("Idempotency-Replayed") == "true"
}

func balance(walletID string) int64 {
	body := mustGet(fmt.Sprintf("/wallets/%s/balance", walletID))
	return int64(body["balance_minor"].(float64))
}

// reportOrder shows how a real caller classifies the response.
func reportOrder(orderID string, status int, body map[string]any) {
	switch status {
	case http.StatusOK:
		fmt.Printf("  %s -> ACCEPTED   (balance ₹%s)\n", orderID, rupees(int64(body["balance_minor"].(float64))))
	case http.StatusPaymentRequired:
		fmt.Printf("  %s -> REJECTED   (%s) -> order cancelled\n", orderID, code(body))
	default:
		fmt.Printf("  %s -> transient? status=%d -> a real caller would retry\n", orderID, status)
	}
}

// ---- tiny HTTP helpers ----

func doJSON(method, path string, payload any) (int, map[string]any, http.Header) {
	var reader io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL()+path, reader)
	if err != nil {
		fatalf("build request: %v", err)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Customer-Id", demoCustomer) // simulates auth-injected caller identity
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatalf("request %s %s: %v (is the wallet service running?)", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header
}

func mustPost(path string, payload any) map[string]any {
	status, body, _ := doJSON(http.MethodPost, path, payload)
	if status < 200 || status >= 300 {
		fatalf("POST %s: status=%d body=%v", path, status, body)
	}
	return body
}

func mustGet(path string) map[string]any {
	status, body, _ := doJSON(http.MethodGet, path, nil)
	if status != http.StatusOK {
		fatalf("GET %s: status=%d body=%v", path, status, body)
	}
	return body
}

// ---- formatting ----

func rupees(minor int64) string { return fmt.Sprintf("%.2f", float64(minor)/100) }
func signedRupees(minor float64) string {
	if minor >= 0 {
		return "+" + fmt.Sprintf("%.2f", minor/100)
	}
	return fmt.Sprintf("%.2f", minor/100)
}

func code(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return "?"
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "orderstub: "+format+"\n", a...)
	os.Exit(1)
}
