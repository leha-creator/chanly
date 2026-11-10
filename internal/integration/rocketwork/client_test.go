package rocketwork_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/integration/rocketwork"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
	"github.com/leha-creator/chanly/internal/platform/outbound"
	"github.com/leha-creator/chanly/internal/platform/ratelimit"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

const token = "rw-token-secret"

func newClient(t *testing.T, h http.HandlerFunc) (*rocketwork.Client, *dbtest.DB) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	d := dbtest.New(t)
	clk := clock.NewManual(time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC))
	log := logx.Discard()
	noSleep := func(context.Context, time.Duration) error { return nil }
	out := outbound.New(d.Pool, clk, log, outbound.Options{System: outbound.SystemRocketWork, Sleep: noSleep})
	lim := ratelimit.New(d.Pool, clk, log, ratelimit.Options{Sleep: func(_ context.Context, w time.Duration) error {
		clk.Advance(w)
		return nil
	}})
	return rocketwork.New(out, lim, rocketwork.Options{BaseURL: srv.URL, Token: token}, log), d
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestPublicAPIReads(t *testing.T) {
	validate := contractValidator(t)
	checkedResponse := func(w http.ResponseWriter, schema string, body any) {
		if err := validate("/components/schemas/"+schema, body); err != nil {
			t.Errorf("HTTP response violates %s: %v", schema, err)
		}
		writeJSON(w, 200, body)
	}
	c, d := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer "+token ||
			r.Header.Get("Accept") != "application/json" || r.Header.Get("User-Agent") == "" {
			t.Error("invalid request method or headers")
		}
		if r.Header.Get("Idempotency-Key") != "" {
			t.Error("undocumented idempotency header")
		}
		switch r.URL.Path {
		case "/api/account":
			checkedResponse(w, "AccountDetails", map[string]any{"balance": json.Number("1234.56"), "company_name": "synthetic", "company_inn": "private-inn", "requisites": map[string]string{"account_number": "private-account"}})
		case "/api/tasks/7":
			checkedResponse(w, "Task", map[string]any{"task": map[string]any{"id": 7, "type": "OneTime", "amount_gross": "100.00", "status": "paid", "commission": "3.01", "receipt_uri": "https://private-receipt"}})
		case "/api/tasks":
			if r.URL.Query().Get("external_ids[]") != "po-1" {
				t.Error("wrong external id filter")
			}
			checkedResponse(w, "Tasks", map[string]any{"meta": map[string]int{"current": 1, "last": 1, "total": 1}, "tasks": []any{map[string]any{"id": 7, "type": "OneTime", "amount_gross": "100.00", "external_id": "po-1", "status": "error"}}})
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	balance, err := c.GetDepositBalance(ctx)
	if err != nil || balance != 123456 {
		t.Fatalf("balance %d: %v", balance, err)
	}
	p, err := c.GetPayout(ctx, "7")
	if err != nil || p.Status != rocketwork.PaymentSucceeded || p.FeeKopecks != 301 {
		t.Fatalf("payment %+v: %v", p, err)
	}
	p, found, err := c.FindPayout(ctx, "rw-payout:po-1")
	if err != nil || !found || p.Status != rocketwork.PaymentSent {
		t.Fatalf("recover %+v %v: %v", p, found, err)
	}
	var recorded string
	if err := d.Pool.QueryRow(ctx, "SELECT string_agg(response_body::text, ' ') FROM outbound_request").Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{token, "private-inn", "private-account", "private-receipt", "private-error"} {
		if strings.Contains(recorded, secret) {
			t.Errorf("stored sensitive field %s", secret)
		}
	}
}

func TestLookupFailsClosed(t *testing.T) {
	var response any
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, response) })
	task := map[string]any{"id": 7, "type": "OneTime", "external_id": "po-1", "status": "paying"}
	for _, v := range []any{
		map[string]any{}, map[string]any{"tasks": nil},
		map[string]any{"tasks": []any{task, task}},
		map[string]any{"tasks": []any{map[string]any{"id": 7, "type": "OneTime", "external_id": "wrong", "status": "paid"}}},
	} {
		response = v
		if _, found, err := c.FindPayout(context.Background(), "rw-payout:po-1"); !errors.Is(err, rocketwork.ErrFormat) || found {
			t.Fatalf("ambiguous lookup: %v %v", found, err)
		}
	}
	response = map[string]any{"tasks": []any{}, "meta": map[string]int{"current": 1, "last": 1, "total": 0}}
	if _, found, err := c.FindPayout(context.Background(), "rw-payout:po-1"); err != nil || found {
		t.Fatalf("empty lookup: %v %v", found, err)
	}
}

func TestUnsupportedOperationsNeverCallProvider(t *testing.T) {
	// Nil dependencies prove that these paths cannot reach the network or database.
	c := rocketwork.New(nil, nil, rocketwork.Options{}, logx.Discard())
	ctx := context.Background()
	if _, err := c.EnsureContractor(ctx, rocketwork.ContractorRequest{}); !errors.Is(err, rocketwork.ErrDisabled) {
		t.Fatal(err)
	}
	if _, err := c.GetContractor(ctx, "123"); !errors.Is(err, rocketwork.ErrDisabled) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.CreatePayout(ctx, rocketwork.PaymentRequest{}); !errors.Is(err, rocketwork.ErrDisabled) {
			t.Fatal(err)
		}
	}
	body := rocketwork.WebhookPayload("ev", rocketwork.ServiceEventPayment, "7")
	if _, err := c.ParseWebhook(http.Header{rocketwork.SignatureHeader: {rocketwork.Sign([]byte("secret"), body)}}, body); !errors.Is(err, rocketwork.ErrSignature) {
		t.Fatal(err)
	}
}

func TestReadErrors(t *testing.T) {
	status := 401
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, status, map[string]any{"message": "private-error"})
	})
	for _, code := range []int{401, 404, 422, 502} {
		status = code
		_, err := c.GetDepositBalance(context.Background())
		if err == nil || strings.Contains(err.Error(), "private-error") {
			t.Fatalf("unsafe error: %v", err)
		}
		if code == 404 && !errors.Is(err, rocketwork.ErrNotFound) {
			t.Fatal(err)
		}
		if code == 502 && !errors.Is(err, rocketwork.ErrUnavailable) {
			t.Fatal(err)
		}
	}
}

func TestLookupRejectsDuplicateAcrossPages(t *testing.T) {
	calls := 0
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		page := 1
		if r.URL.Query().Get("page") == "2" {
			page = 2
		}
		writeJSON(w, 200, map[string]any{"tasks": []any{map[string]any{"id": 7, "type": "OneTime", "status": "paying", "amount_gross": "100.00", "external_id": "same"}}, "meta": map[string]int{"current": page, "last": 2, "total": 2}})
	})
	if _, found, err := c.FindPayout(context.Background(), "rw-payout:same"); !errors.Is(err, rocketwork.ErrFormat) || found || calls != 2 {
		t.Fatalf("duplicate: found=%t calls=%d err=%v", found, calls, err)
	}
}

func TestClientRejectsRedirect(t *testing.T) {
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	client := rocketwork.NewHTTPClient()
	defer client.CloseIdleConnections()
	resp, err := client.Get(redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || targetCalls != 0 {
		t.Fatal("redirect followed")
	}
}
