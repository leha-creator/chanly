package httpx_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/httpx"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

func TestRequireAdmin(t *testing.T) {
	log := logx.Discard()
	if _, err := httpx.RequireAdmin(context.Background(), log, "Test", "moderator"); !apperr.Is(err, apperr.CodeUnauthorized) {
		t.Fatalf("no session: %v", err)
	}
	for role, ok := range map[string]bool{"": false, "moderator": true, "senior": true, "owner": false} {
		ctx := httpx.WithPrincipal(context.Background(), &httpx.Principal{UserID: uuid.New(), AdminRole: role})
		_, err := httpx.RequireAdmin(ctx, log, "Test", "moderator", "senior")
		if ok && err != nil {
			t.Errorf("%q: %v", role, err)
		}
		if !ok {
			ae, isApp := apperr.As(err)
			if !isApp || ae.Code != apperr.CodeForbidden || ae.Details["reason"] != httpx.ReasonAdminRequired {
				t.Errorf("%q: %v", role, err)
			}
		}
	}
}

func TestAdminSeniorOnly(t *testing.T) {
	log := logx.Discard()
	for role, ok := range map[string]bool{"moderator": false, "senior": true} {
		p := &httpx.Principal{UserID: uuid.New(), AdminRole: role}
		ctx := httpx.WithPrincipal(context.Background(), p)
		_, err := httpx.RequireAdmin(ctx, log, "Test", httpx.AdminSeniorOnly...)
		if ok != (err == nil) {
			t.Errorf("%s on senior-only: %v", role, err)
		}
		if _, err := httpx.RequireAdmin(ctx, log, "Test", httpx.AdminAny...); err != nil {
			t.Errorf("%s on any: %v", role, err)
		}
		inn, email := httpx.INNFor(p, "7712345678"), httpx.EmailFor(p, "ivan@example.ru")
		if ok && (inn != "7712345678" || email != "ivan@example.ru") {
			t.Errorf("senior sees %s %s", inn, email)
		}
		if !ok && (inn != "77****5678" || email != "i***@example.ru") {
			t.Errorf("moderator sees %s %s", inn, email)
		}
	}
}
