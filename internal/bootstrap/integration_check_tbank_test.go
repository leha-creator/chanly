package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leha-creator/chanly/internal/billing/domain"
	"github.com/leha-creator/chanly/internal/platform/clock"
	"github.com/leha-creator/chanly/internal/platform/dbtest"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

type bankCheckSourceFunc func(context.Context, domain.BankQuery) (domain.BankPage, error)

func (f bankCheckSourceFunc) ReadPage(c context.Context, q domain.BankQuery) (domain.BankPage, error) {
	return f(c, q)
}

func TestTBankDiagnosticRecordsStaticCursorCycle(t *testing.T) {
	in := &infra{log: logx.Discard()}
	state := bankCheckState{Status: "started"}
	calls, saves := 0, 0
	source := bankCheckSourceFunc(func(_ context.Context, q domain.BankQuery) (domain.BankPage, error) {
		calls++
		if calls == 2 && q.Cursor != "static" {
			t.Fatal("cursor not forwarded")
		}
		return domain.BankPage{Operations: 1, Rejected: 1, NextCursor: "static"}, nil
	})
	err := in.readTBankCheck(context.Background(), source, Config{}, time.Now().Add(-time.Hour), time.Now(), &state, func() error { saves++; return nil })
	if err == nil || state.Status != "cursor_cycle" || state.Complete || calls != 2 || saves != 1 || state.Incoming != 0 {
		t.Fatalf("static cycle state %+v, calls=%d saves=%d err=%v", state, calls, saves, err)
	}
}

func TestTBankDiagnosticRecordsReadFailure(t *testing.T) {
	in := &infra{log: logx.Discard()}
	state := bankCheckState{Status: "started"}
	saves := 0
	source := bankCheckSourceFunc(func(context.Context, domain.BankQuery) (domain.BankPage, error) {
		return domain.BankPage{}, errors.New("read denied")
	})
	err := in.readTBankCheck(context.Background(), source, Config{}, time.Now().Add(-time.Hour), time.Now(), &state, func() error { saves++; return nil })
	if err == nil || state.Status != "interrupted" || state.Complete || saves != 1 {
		t.Fatalf("failed state %+v %v", state, err)
	}
}

func TestTBankDiagnosticAccessDenialWithoutAlertSender(t *testing.T) {
	d := dbtest.New(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"errorCode":"UNAUTHORIZED","errorMessage":"test access denied"}`))
	}))
	defer server.Close()
	in := &infra{pool: d.Pool, clock: clock.System{}, log: logx.Discard()}
	cfg := testConfig()
	cfg.TBank.Mode = "live"
	cfg.TBank.BaseURL = server.URL
	cfg.TBank.Token = "test-invalid-token"
	cfg.Billing.PlatformBankAccount = "40802810838000000001"
	source, err := in.bankSource(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ReadPage(context.Background(), domain.BankQuery{Account: cfg.Billing.PlatformBankAccount, From: time.Now().Add(-time.Hour), To: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), cfg.TBank.Token) {
		t.Fatalf("expected safe 401 rejection, got %v", err)
	}
	var status int
	var request string
	if err := d.Pool.QueryRow(context.Background(), "SELECT response_status, request::text FROM outbound_request WHERE system='tbank'").Scan(&status, &request); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusUnauthorized || strings.Contains(request, cfg.TBank.Token) || strings.Contains(request, cfg.Billing.PlatformBankAccount) {
		t.Fatalf("unsafe access-denial record, status=%d", status)
	}
}
