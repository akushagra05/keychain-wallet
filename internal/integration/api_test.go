package integration

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

func TestDeduct_HappyPath(t *testing.T) {
	walletID := createWallet(t)
	topup(t, walletID, "pay_"+walletID, 50000) // ₹500

	s, b, h := post(t, deductPath(walletID), map[string]any{"order_id": "order_" + walletID})
	if s != http.StatusOK {
		t.Fatalf("expected 200, got %d body %v", s, b)
	}
	if h.Get("Idempotency-Replayed") == "true" {
		t.Fatalf("first deduct must not be a replay")
	}
	if got := int64(b["balance_minor"].(float64)); got != 40000 {
		t.Fatalf("expected balance 40000, got %d", got)
	}
	entry := b["entry"].(map[string]any)
	if int64(entry["amount_minor"].(float64)) != -deductAmount {
		t.Fatalf("expected signed amount -%d, got %v", deductAmount, entry["amount_minor"])
	}
	if int64(entry["balance_after"].(float64)) != 40000 {
		t.Fatalf("expected balance_after 40000, got %v", entry["balance_after"])
	}
}

// A deduct on an empty wallet is rejected with 402, writes no ledger entry, and
// crucially does NOT burn the idempotency key — a retry after a top-up succeeds.
func TestDeduct_InsufficientBalance_KeyNotBurned(t *testing.T) {
	walletID := createWallet(t) // zero balance
	orderID := "order_" + walletID

	s, b, _ := post(t, deductPath(walletID), map[string]any{"order_id": orderID})
	if s != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d", s)
	}
	if errCode(b) != "INSUFFICIENT_BALANCE" {
		t.Fatalf("expected INSUFFICIENT_BALANCE, got %q", errCode(b))
	}
	if c := ledgerCount(t, walletID, "DEDUCT"); c != 0 {
		t.Fatalf("rejected deduct must write no ledger entry, found %d", c)
	}

	// Top up, then retry the SAME order id: it must now succeed (key not burned).
	topup(t, walletID, "pay_"+walletID, deductAmount)
	s2, _, h2 := post(t, deductPath(walletID), map[string]any{"order_id": orderID})
	if s2 != http.StatusOK {
		t.Fatalf("retry after top-up should succeed, got %d", s2)
	}
	if h2.Get("Idempotency-Replayed") == "true" {
		t.Fatalf("retry after a rejected deduct is a fresh op, not a replay")
	}
}

func TestDeduct_Boundary(t *testing.T) {
	walletID := createWallet(t)
	topup(t, walletID, "pay_"+walletID, deductAmount) // exactly one order's worth

	if s, _, _ := post(t, deductPath(walletID), map[string]any{"order_id": "o1_" + walletID}); s != http.StatusOK {
		t.Fatalf("first deduct at exact balance should succeed, got %d", s)
	}
	if s, _, _ := post(t, deductPath(walletID), map[string]any{"order_id": "o2_" + walletID}); s != http.StatusPaymentRequired {
		t.Fatalf("second deduct should be 402, got %d", s)
	}
	if bal := getBalance(t, walletID); bal != 0 {
		t.Fatalf("expected balance 0, got %d", bal)
	}
}

// Sequential replay: same order twice -> one deduction, identical entry returned,
// second response flagged as a replay.
func TestDeduct_Idempotent_Sequential(t *testing.T) {
	walletID := createWallet(t)
	topup(t, walletID, "pay_"+walletID, 50000)
	orderID := "order_seq_" + walletID

	_, b1, h1 := post(t, deductPath(walletID), map[string]any{"order_id": orderID})
	if h1.Get("Idempotency-Replayed") == "true" {
		t.Fatalf("first call should not be a replay")
	}
	_, b2, h2 := post(t, deductPath(walletID), map[string]any{"order_id": orderID})
	if h2.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("second call should be a replay")
	}
	if entryID(b1) != entryID(b2) {
		t.Fatalf("replay returned a different entry: %s vs %s", entryID(b1), entryID(b2))
	}
	if bal := getBalance(t, walletID); bal != 40000 {
		t.Fatalf("replay must not change the balance, got %d", bal)
	}
	if c := ledgerCount(t, walletID, "DEDUCT"); c != 1 {
		t.Fatalf("expected 1 DEDUCT entry, got %d", c)
	}
}

func TestTopup_Idempotent(t *testing.T) {
	walletID := createWallet(t)
	ref := "pay_dup_" + walletID

	topup(t, walletID, ref, 50000)
	s, _, h := post(t, topupPath(walletID), map[string]any{"amount_minor": 50000, "payment_ref": ref})
	if s != http.StatusOK || h.Get("Idempotency-Replayed") != "true" {
		t.Fatalf("duplicate topup should replay: status=%d replayed=%q", s, h.Get("Idempotency-Replayed"))
	}
	if bal := getBalance(t, walletID); bal != 50000 {
		t.Fatalf("double credit! expected 50000, got %d", bal)
	}
	if c := ledgerCount(t, walletID, "TOPUP"); c != 1 {
		t.Fatalf("expected 1 TOPUP entry, got %d", c)
	}
}

// Same idempotency key used against a different wallet -> 409, not a silent replay.
func TestIdempotency_CrossWalletConflict(t *testing.T) {
	w1, w2 := createWallet(t), createWallet(t)
	topup(t, w1, "pay_"+w1, 50000)
	topup(t, w2, "pay_"+w2, 50000)
	orderID := "shared_" + w1

	if s, _, _ := post(t, deductPath(w1), map[string]any{"order_id": orderID}); s != http.StatusOK {
		t.Fatalf("first deduct should succeed, got %d", s)
	}
	s, b, _ := post(t, deductPath(w2), map[string]any{"order_id": orderID})
	if s != http.StatusConflict {
		t.Fatalf("expected 409 for cross-wallet key reuse, got %d", s)
	}
	if errCode(b) != "IDEMPOTENCY_KEY_CONFLICT" {
		t.Fatalf("expected IDEMPOTENCY_KEY_CONFLICT, got %q", errCode(b))
	}
}

func TestWalletNotFound(t *testing.T) {
	fake := uuid.NewString()
	if s, b, _ := get(t, "/wallets/"+fake+"/balance"); s != http.StatusNotFound || errCode(b) != "WALLET_NOT_FOUND" {
		t.Fatalf("balance on missing wallet: status=%d code=%q", s, errCode(b))
	}
	if s, _, _ := post(t, deductPath(fake), map[string]any{"order_id": "x"}); s != http.StatusNotFound {
		t.Fatalf("deduct on missing wallet: expected 404, got %d", s)
	}
	if s, _, _ := post(t, topupPath(fake), map[string]any{"amount_minor": 100, "payment_ref": "p"}); s != http.StatusNotFound {
		t.Fatalf("topup on missing wallet: expected 404, got %d", s)
	}
}

func TestValidation(t *testing.T) {
	walletID := createWallet(t)
	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"deduct missing order_id", http.MethodPost, deductPath(walletID), map[string]any{}},
		{"topup zero amount", http.MethodPost, topupPath(walletID), map[string]any{"amount_minor": 0, "payment_ref": "p"}},
		{"topup negative amount", http.MethodPost, topupPath(walletID), map[string]any{"amount_minor": -5, "payment_ref": "p"}},
		{"topup missing payment_ref", http.MethodPost, topupPath(walletID), map[string]any{"amount_minor": 100}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, b, _, err := rawJSON(c.method, c.path, c.body)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if s != http.StatusBadRequest || errCode(b) != "VALIDATION_ERROR" {
				t.Fatalf("expected 400 VALIDATION_ERROR, got %d %q", s, errCode(b))
			}
		})
	}

	// Malformed UUID in the path -> 400.
	if s, _, _ := get(t, "/wallets/not-a-uuid/balance"); s != http.StatusBadRequest {
		t.Fatalf("malformed wallet id: expected 400, got %d", s)
	}
}

// The auth seam: per-wallet operations require an X-Customer-Id (401 without it),
// while balance is intentionally left open per the spec ("anyone").
func TestAuth_MissingIdentity(t *testing.T) {
	walletID := createWallet(t) // created WITH identity via the helper

	protected := []struct {
		name    string
		method  string
		path    string
		payload any
	}{
		{"create", http.MethodPost, "/wallets", map[string]any{}},
		{"topup", http.MethodPost, topupPath(walletID), map[string]any{"amount_minor": 100, "payment_ref": "p"}},
		{"deduct", http.MethodPost, deductPath(walletID), map[string]any{"order_id": "o"}},
		{"transactions", http.MethodGet, "/wallets/" + walletID + "/transactions", nil},
	}
	for _, c := range protected {
		t.Run(c.name, func(t *testing.T) {
			s, b, _, err := doReq(c.method, c.path, "", c.payload) // "" -> no X-Customer-Id header
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			if s != http.StatusUnauthorized || errCode(b) != "UNAUTHENTICATED" {
				t.Fatalf("expected 401 UNAUTHENTICATED, got %d %q", s, errCode(b))
			}
		})
	}

	// Balance stays open (spec: "anyone") — 200 even without identity.
	if s, _, _, _ := doReq(http.MethodGet, "/wallets/"+walletID+"/balance", "", nil); s != http.StatusOK {
		t.Fatalf("balance should be open (200) without identity, got %d", s)
	}
}

// The core invariant after an arbitrary mix of operations: the materialized
// balance always equals the sum of the ledger.
func TestInvariant_BalanceEqualsLedgerSum(t *testing.T) {
	walletID := createWallet(t)
	topup(t, walletID, "p1_"+walletID, 100000)
	topup(t, walletID, "p2_"+walletID, 50000)
	for i := 0; i < 7; i++ {
		post(t, deductPath(walletID), map[string]any{"order_id": fmt.Sprintf("inv_%s_%d", walletID, i)})
	}
	bal, sum := getBalance(t, walletID), ledgerSum(t, walletID)
	if bal != sum {
		t.Fatalf("invariant broken: balance %d != ledger sum %d", bal, sum)
	}
	if bal != 150000-7*deductAmount {
		t.Fatalf("expected balance %d, got %d", 150000-7*deductAmount, bal)
	}
}

func TestListTransactions_Pagination(t *testing.T) {
	walletID := createWallet(t)
	topup(t, walletID, "pp_"+walletID, 100000)
	for i := 0; i < 5; i++ {
		post(t, deductPath(walletID), map[string]any{"order_id": fmt.Sprintf("pg_%s_%d", walletID, i)})
	}
	// 1 topup + 5 deducts = 6 entries; page size 2 -> 3 pages.
	seen, pages, cursor := 0, 0, ""
	for {
		path := fmt.Sprintf("/wallets/%s/transactions?limit=2", walletID)
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		_, b, _ := get(t, path)
		seen += len(b["data"].([]any))
		pages++
		if b["next_cursor"] == nil {
			break
		}
		cursor = b["next_cursor"].(string)
		if pages > 10 {
			t.Fatal("pagination did not terminate")
		}
	}
	if seen != 6 {
		t.Fatalf("expected 6 entries across pages, got %d", seen)
	}
}

func TestListTransactions_ReferenceFilter(t *testing.T) {
	walletID := createWallet(t)
	topup(t, walletID, "rf_"+walletID, 50000)
	order := "findme_" + walletID
	post(t, deductPath(walletID), map[string]any{"order_id": order})

	_, b, _ := get(t, fmt.Sprintf("/wallets/%s/transactions?reference=%s&type=DEDUCT", walletID, url.QueryEscape(order)))
	data := b["data"].([]any)
	if len(data) != 1 {
		t.Fatalf("reference filter expected 1 entry, got %d", len(data))
	}
	if data[0].(map[string]any)["reference"] != order {
		t.Fatalf("reference filter returned wrong entry")
	}
}

// The DB-level CHECK is the last-line backstop: even a raw UPDATE that bypasses
// all application logic cannot drive a balance negative.
func TestCheckConstraint_BlocksNegativeBalance(t *testing.T) {
	walletID := createWallet(t)
	_, err := testPool.Exec(context.Background(),
		`UPDATE wallets SET balance_minor = -1 WHERE id = $1::uuid`, walletID)
	if err == nil {
		t.Fatal("expected CHECK constraint to reject a negative balance")
	}
}
