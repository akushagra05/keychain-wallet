// Package integration holds end-to-end tests that exercise the full stack
// (HTTP handler -> service -> repository -> Postgres) against a throwaway
// Postgres spun up by testcontainers. A real database is non-negotiable here:
// the correctness we care about (concurrency + idempotency) cannot be faithfully
// mocked.
//
// One container is shared across all tests (TestMain); tests isolate themselves
// by creating their own wallets and using unique idempotency keys.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"keychain-wallet/internal/db"
	"keychain-wallet/internal/handlers"
	"keychain-wallet/internal/repository"
	"keychain-wallet/internal/service"
)

const deductAmount = 10000 // ₹100, matches the service default

var (
	testPool   *pgxpool.Pool
	testServer *httptest.Server
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pgC, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("wallet"),
		postgres.WithUsername("wallet"),
		postgres.WithPassword("wallet"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start postgres (is Docker running?): %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = testcontainers.TerminateContainer(pgC) }()

	dbURL, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		die("conn string", err)
	}
	if err := db.Migrate(dbURL); err != nil {
		die("migrate", err)
	}
	testPool, err = pgxpool.New(ctx, dbURL)
	if err != nil {
		die("pool", err)
	}

	repo := repository.New(testPool)
	svc := service.New(repo, deductAmount)
	h := handlers.New(svc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	testServer = httptest.NewServer(h.Routes())

	code := m.Run()

	testServer.Close()
	testPool.Close()
	os.Exit(code)
}

func die(msg string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", msg, err)
	os.Exit(1)
}

// testCustomer is the caller identity sent by default on every request.
const testCustomer = "cust_test"

// ---- low-level HTTP (goroutine-safe: returns errors instead of calling t) ----

// doReq sends a request with the given caller identity (X-Customer-Id). An empty
// caller omits the header — used to exercise the 401 path.
func doReq(method, path, caller string, payload any) (int, map[string]any, http.Header, error) {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, testServer.URL+path, body)
	if err != nil {
		return 0, nil, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if caller != "" {
		req.Header.Set("X-Customer-Id", caller)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp.Header, nil
}

// rawJSON sends a request as the default test customer.
func rawJSON(method, path string, payload any) (int, map[string]any, http.Header, error) {
	return doReq(method, path, testCustomer, payload)
}

// ---- test-friendly wrappers (fail the test on transport error) ----

func post(t *testing.T, path string, payload any) (int, map[string]any, http.Header) {
	t.Helper()
	s, b, h, err := rawJSON(http.MethodPost, path, payload)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return s, b, h
}

func get(t *testing.T, path string) (int, map[string]any, http.Header) {
	t.Helper()
	s, b, h, err := rawJSON(http.MethodGet, path, nil)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return s, b, h
}

// ---- domain helpers ----

func createWallet(t *testing.T) string {
	t.Helper()
	s, b, _ := post(t, "/wallets", map[string]any{}) // owner comes from X-Customer-Id (via rawJSON)
	if s != http.StatusCreated {
		t.Fatalf("create wallet: status %d body %v", s, b)
	}
	return b["id"].(string)
}

func topup(t *testing.T, walletID, paymentRef string, amountMinor int64) {
	t.Helper()
	s, b, _ := post(t, topupPath(walletID), map[string]any{"amount_minor": amountMinor, "payment_ref": paymentRef})
	if s != http.StatusOK {
		t.Fatalf("topup: status %d body %v", s, b)
	}
}

func getBalance(t *testing.T, walletID string) int64 {
	t.Helper()
	s, b, _ := get(t, "/wallets/"+walletID+"/balance")
	if s != http.StatusOK {
		t.Fatalf("balance: status %d body %v", s, b)
	}
	return int64(b["balance_minor"].(float64))
}

func topupPath(walletID string) string  { return "/wallets/" + walletID + "/topup" }
func deductPath(walletID string) string { return "/wallets/" + walletID + "/deduct" }

// ---- assertions against the DB directly ----

func ledgerCount(t *testing.T, walletID, txType string) int {
	t.Helper()
	var n int
	err := testPool.QueryRow(context.Background(),
		`SELECT count(*) FROM ledger_entries WHERE wallet_id = $1::uuid AND type = $2`, walletID, txType).Scan(&n)
	if err != nil {
		t.Fatalf("ledger count: %v", err)
	}
	return n
}

func ledgerSum(t *testing.T, walletID string) int64 {
	t.Helper()
	var sum int64
	err := testPool.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(amount_minor), 0) FROM ledger_entries WHERE wallet_id = $1::uuid`, walletID).Scan(&sum)
	if err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	return sum
}

func errCode(body map[string]any) string {
	if e, ok := body["error"].(map[string]any); ok {
		if c, ok := e["code"].(string); ok {
			return c
		}
	}
	return ""
}

func entryID(body map[string]any) string {
	if e, ok := body["entry"].(map[string]any); ok {
		if id, ok := e["id"].(string); ok {
			return id
		}
	}
	return ""
}
