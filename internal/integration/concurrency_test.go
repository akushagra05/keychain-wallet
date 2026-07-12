package integration

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
)

// TestDeduct_Concurrency is the correctness centerpiece. N distinct orders race
// for a wallet funded for only M deductions. Exactly M must succeed, the rest
// must be rejected, the balance must land at 0 (never negative), and the ledger
// must contain exactly M debit entries. This is what proves the balance
// constraint holds under concurrency.
func TestDeduct_Concurrency(t *testing.T) {
	const (
		funded = 5  // wallet funds exactly 5 deductions
		racers = 25 // 25 concurrent, distinct orders
	)
	walletID := createWallet(t)
	topup(t, walletID, "pay_"+walletID, funded*deductAmount)

	statuses := make([]int, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release all goroutines simultaneously for maximum contention
			s, _, _, err := rawJSON(http.MethodPost, deductPath(walletID),
				map[string]any{"order_id": fmt.Sprintf("order_%s_%d", walletID, i)})
			if err != nil {
				statuses[i] = -1
				return
			}
			statuses[i] = s
		}(i)
	}
	close(start)
	wg.Wait()

	var ok, insufficient int
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusPaymentRequired:
			insufficient++
		default:
			t.Fatalf("unexpected status %d", s)
		}
	}

	if ok != funded {
		t.Errorf("expected exactly %d successful deducts, got %d", funded, ok)
	}
	if insufficient != racers-funded {
		t.Errorf("expected %d rejections, got %d", racers-funded, insufficient)
	}
	if bal := getBalance(t, walletID); bal != 0 {
		t.Errorf("expected final balance 0, got %d (never-negative invariant)", bal)
	}
	if c := ledgerCount(t, walletID, "DEDUCT"); c != funded {
		t.Errorf("expected %d DEDUCT ledger entries, got %d", funded, c)
	}
	// balance == SUM(ledger): funded topup - funded deducts == 0
	if sum := ledgerSum(t, walletID); sum != 0 {
		t.Errorf("ledger sum %d != balance 0", sum)
	}
}

// TestDeduct_Idempotent_Concurrent fires the SAME order concurrently many times
// against a well-funded wallet. Exactly one deduction must occur; every request
// must observe the same (replayed) balance. This proves guard-first idempotency
// holds even when duplicates arrive simultaneously.
func TestDeduct_Idempotent_Concurrent(t *testing.T) {
	const racers = 20
	walletID := createWallet(t)
	topup(t, walletID, "pay_"+walletID, 10*deductAmount) // plenty of funds
	orderID := "dup_order_" + walletID

	okBalances := make([]int64, racers)
	statuses := make([]int, racers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s, b, _, err := rawJSON(http.MethodPost, deductPath(walletID), map[string]any{"order_id": orderID})
			if err != nil {
				statuses[i] = -1
				return
			}
			statuses[i] = s
			if s == http.StatusOK {
				okBalances[i] = int64(b["balance_minor"].(float64))
			}
		}(i)
	}
	close(start)
	wg.Wait()

	// Exactly one real deduction — the rest are replays.
	if c := ledgerCount(t, walletID, "DEDUCT"); c != 1 {
		t.Fatalf("expected exactly 1 DEDUCT entry, got %d (double-charge!)", c)
	}
	want := int64(9 * deductAmount) // 10 funded - 1 deducted
	if bal := getBalance(t, walletID); bal != want {
		t.Fatalf("expected balance %d, got %d", want, bal)
	}
	for i, s := range statuses {
		if s != http.StatusOK {
			t.Fatalf("request %d: expected 200 (real or replay), got %d", i, s)
		}
		if okBalances[i] != want {
			t.Fatalf("request %d reported balance %d, want %d (replay must return original result)", i, okBalances[i], want)
		}
	}
}
