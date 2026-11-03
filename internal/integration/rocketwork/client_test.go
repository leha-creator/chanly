package rocketwork_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	return rocketwork.New(out, lim, rocketwork.Options{BaseURL: srv.URL, Token: token, WebhookSecret: []byte("s")}, log), d
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestEnsureAndGetContractor(t *testing.T) {
	var gotBody map[string]string
	c, d := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/contractors":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotBody)
			writeJSON(w, http.StatusCreated, map[string]string{"id": "c-1", "status": "invited", "onboarding_url": "https://o/c-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/contractors/c-1":
			writeJSON(w, http.StatusOK, map[string]string{"id": "c-1", "status": "fns_unbound"})
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	got, err := c.EnsureContractor(ctx, rocketwork.ContractorRequest{ExternalID: "org-1", INN: "500100732259",
		FullName: "Иванов Иван", Phone: "+79991234567", LegalForm: rocketwork.LegalFormSelfEmployed})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "c-1" || got.Status != rocketwork.ContractorInvited || got.OnboardingURL != "https://o/c-1" {
		t.Fatalf("contractor %+v", got)
	}
	if gotBody["inn"] != "500100732259" || gotBody["legal_form"] != "self_employed" || gotBody["external_id"] != "org-1" {
		t.Fatalf("request body %v", gotBody)
	}
	got, err = c.GetContractor(ctx, "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != rocketwork.ContractorBlocked || got.BlockedReason != rocketwork.FailureTaxStatusLost {
		t.Fatalf("blocked contractor %+v", got)
	}
	// В outbound_request ИНН, телефон и ФИО маскированы, токена нет.
	var recorded string
	if err := d.Pool.QueryRow(ctx, `SELECT request::text
		FROM outbound_request WHERE operation = 'RocketWork.EnsureContractor'`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"500100732259", "+79991234567", "Иванов Иван", token} {
		if strings.Contains(recorded, secret) {
			t.Errorf("outbound_request contains %q: %s", secret, recorded)
		}
	}
}

func TestPayouts(t *testing.T) {
	var key string
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/account/balance":
			writeJSON(w, http.StatusOK, map[string]any{"available_kopecks": 123456, "currency": "RUB"})
		case r.Method == http.MethodPost && r.URL.Path == "/payments":
			key = r.Header.Get("Idempotency-Key")
			writeJSON(w, http.StatusCreated, map[string]any{"id": "p-1", "external_id": "po-1", "status": "processing"})
		case r.Method == http.MethodGet && r.URL.Path == "/payments/p-1":
			writeJSON(w, http.StatusOK, map[string]any{"id": "p-1", "status": "paid", "fee_kopecks": 300, "receipt_url": "https://r/1"})
		case r.Method == http.MethodGet && r.URL.Path == "/payments/p-2":
			writeJSON(w, http.StatusOK, map[string]any{"id": "p-2", "status": "rejected", "failure_reason": "fns_unbound"})
		case r.Method == http.MethodGet && r.URL.Path == "/payments/p-3":
			writeJSON(w, http.StatusOK, map[string]any{"id": "p-3", "status": "on_hold_by_bank"})
		case r.Method == http.MethodGet && r.URL.Path == "/payments":
			if r.URL.Query().Get("external_id") == "po-1" {
				writeJSON(w, http.StatusOK, map[string]any{"items": []any{map[string]any{"id": "p-1", "status": "processing"}}})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	bal, err := c.GetDepositBalance(ctx)
	if err != nil || bal != 123456 {
		t.Fatalf("balance %d, %v", bal, err)
	}
	p, err := c.CreatePayout(ctx, rocketwork.PaymentRequest{IdempotencyKey: "rw-payout:po-1", ExternalID: "po-1",
		ContractorID: "c-1", AmountKopecks: 10000, Purpose: "заказ 1"})
	if err != nil || p.ID != "p-1" || p.Status != rocketwork.PaymentSent || key != "rw-payout:po-1" {
		t.Fatalf("create %+v %v key %q", p, err, key)
	}
	if p, err = c.GetPayout(ctx, "p-1"); err != nil || p.Status != rocketwork.PaymentSucceeded || p.FeeKopecks != 300 {
		t.Fatalf("paid %+v %v", p, err)
	}
	if p, err = c.GetPayout(ctx, "p-2"); err != nil || p.Status != rocketwork.PaymentFailed || p.FailureCode != rocketwork.FailureTaxStatusLost {
		t.Fatalf("failed %+v %v", p, err)
	}
	if p, err = c.GetPayout(ctx, "p-3"); err != nil || p.Status != rocketwork.PaymentSent || p.Known {
		t.Fatalf("unknown status %+v %v", p, err)
	}
	if _, found, err := c.FindPayout(ctx, "rw-payout:po-1"); err != nil || !found {
		t.Fatalf("find %v %v", found, err)
	}
	if _, found, err := c.FindPayout(ctx, "rw-payout:po-9"); err != nil || found {
		t.Fatalf("find missing %v %v", found, err)
	}
	if _, err := c.GetPayout(ctx, "p-404"); !errors.Is(err, rocketwork.ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
}

func TestErrors(t *testing.T) {
	status := http.StatusUnprocessableEntity
	body := map[string]any{"error": map[string]string{"code": "insufficient_funds", "message": "not enough"}}
	c, _ := newClient(t, func(w http.ResponseWriter, r *http.Request) { writeJSON(w, status, body) })
	ctx := context.Background()
	req := rocketwork.PaymentRequest{IdempotencyKey: "rw-payout:x", ExternalID: "x", ContractorID: "c", AmountKopecks: 1}
	_, err := c.CreatePayout(ctx, req)
	var rej *rocketwork.RejectedError
	if !errors.As(err, &rej) || rej.Code != rocketwork.FailureInsufficientDeposit {
		t.Fatalf("422: %v", err)
	}
	status, body = http.StatusUnauthorized, map[string]any{}
	if _, err = c.GetDepositBalance(ctx); !errors.As(err, &rej) || rej.Code != rocketwork.FailureProviderError {
		t.Fatalf("401: %v", err)
	}
	status = http.StatusBadGateway
	if _, err = c.GetDepositBalance(ctx); !errors.Is(err, rocketwork.ErrUnavailable) {
		t.Fatalf("502: %v", err)
	}
}
