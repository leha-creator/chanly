package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	accountdomain "github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

func TestAdminGrant(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	role := accountdomain.AdminModerator
	res, err := a.mods.account.SetAdminRole(ctx, " New.Admin@Example.ru ", &role)
	if err != nil || !res.Created || !res.Changed {
		t.Fatalf("grant: %+v %v", res, err)
	}
	again, err := a.mods.account.SetAdminRole(ctx, "new.admin@example.ru", &role)
	if err != nil || again.Created || again.Changed || again.UserID != res.UserID {
		t.Fatalf("repeat grant: %+v %v", again, err)
	}
	var got *string
	if err := a.db.Pool.QueryRow(ctx, `SELECT admin_role FROM "user" WHERE id = $1`, res.UserID).Scan(&got); err != nil || got == nil || *got != "moderator" {
		t.Fatalf("role %v %v", got, err)
	}
	if res, err := a.mods.account.SetAdminRole(ctx, "new.admin@example.ru", nil); err != nil || !res.Changed {
		t.Fatalf("revoke: %+v %v", res, err)
	}
	if err := a.db.Pool.QueryRow(ctx, `SELECT admin_role FROM "user" WHERE id = $1`, res.UserID).Scan(&got); err != nil || got != nil {
		t.Fatalf("role after revoke %v %v", got, err)
	}
	var n int
	if err := a.db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'user.admin_role_changed' AND actor_type = 'system'
		AND data::text NOT LIKE '%new.admin@example.ru%'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("audit entries %d %v", n, err)
	}
	for _, args := range [][]string{{}, {"grant", "--email", "x@example.ru"}, {"grant", "--email", "x@example.ru", "--role", "owner"}, {"revoke"}, {"bogus"}} {
		if err := runAdmin(ctx, testConfig(), a.mods.in.log, args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
}

// TestCatalogOperationsImplemented — операции этапа «Каталог» больше не отвечают 501.
func TestCatalogOperationsImplemented(t *testing.T) {
	a := newTestApp(t)
	c := a.client()
	c.login("anyone@example.ru")
	id := "0190a000-0000-7000-8000-000000000999"
	for _, op := range []struct{ method, path string }{
		{http.MethodGet, "/placement-formats"},
		{http.MethodGet, "/catalog/channels"},
		{http.MethodGet, "/catalog/channels/" + id},
		{http.MethodGet, "/admin/channels"},
		{http.MethodGet, "/admin/channels/" + id},
		{http.MethodPost, "/admin/channels/" + id + "/approve"},
	} {
		if r := c.do(op.method, op.path, nil); r.status == http.StatusNotImplemented {
			t.Errorf("%s %s: 501", op.method, op.path)
		}
	}
}

// Этап «Маркировка», S-20 до API администратора: maxim admin ord errors|retry.
func TestAdminORD(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	cfg := testConfig()
	cfg.DatabaseURL = a.db.AppURL
	adv := a.advertiserClient("adv.adminord@example.ru")
	org := adv.orgID(t)
	id := ids.New()
	if _, err := a.db.Pool.Exec(ctx, `INSERT INTO ord_organization (id, organization_id, is_rr, ord_external_id, sync_status,
		sync_error, sync_attempts, created_at, updated_at) VALUES ($1, $2, false, $3, 'error', 'ИНН 7707083893 не найден', 1, now(), now())`,
		id, org, org.String()); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runAdmin(ctx, cfg, a.mods.in.log, []string{"ord", "errors"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "organization:"+id.String()) || strings.Contains(out.String(), "7707083893") ||
		!strings.Contains(out.String(), "77****3893") {
		t.Fatalf("errors output %q", out.String())
	}
	ref := "organization:" + id.String()
	if err := runAdmin(ctx, cfg, a.mods.in.log, []string{"ord", "retry", "--object", ref, "--reason", "исправили", "--email", "adv.adminord@example.ru"},
		&bytes.Buffer{}); !errors.Is(err, errNotAdmin) {
		t.Fatalf("retry by non-admin: %v", err)
	}
	role := accountdomain.AdminSenior
	if _, err := a.mods.account.SetAdminRole(ctx, "senior.ord@example.ru", &role); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runAdmin(ctx, cfg, a.mods.in.log, []string{"ord", "retry", "--object", ref, "--reason", "исправили", "--email", "senior.ord@example.ru"},
		&out); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := a.db.Pool.QueryRow(ctx, `SELECT sync_status FROM ord_organization WHERE id = $1`, id).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("status after retry %q %v", status, err)
	}
	for _, args := range [][]string{{"ord"}, {"ord", "bogus"}, {"ord", "retry", "--object", ref}, {"ord", "retry", "--object", "x:y", "--reason", "r", "--email", "e"}} {
		if err := runAdmin(ctx, cfg, a.mods.in.log, args, &bytes.Buffer{}); !errors.Is(err, errUsage) {
			t.Errorf("%v: %v", args, err)
		}
	}
}
