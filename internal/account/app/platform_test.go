package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/accounttest"
	"github.com/leha-creator/chanly/internal/account/app"
	"github.com/leha-creator/chanly/internal/account/domain"
)

func TestEnsurePlatformOrganization(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	inn := accounttest.INN(domain.SoleProprietor)
	in := app.PlatformOrg{INN: inn, Name: "ИП Платформа", LegalType: domain.SoleProprietor}
	id, err := e.svc.EnsurePlatformOrganization(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	var isPlatform bool
	if err := e.d.Pool.QueryRow(ctx, `SELECT name, is_platform FROM organization WHERE id = $1`, id).Scan(&name, &isPlatform); err != nil {
		t.Fatal(err)
	}
	if name != in.Name || !isPlatform {
		t.Fatalf("platform row: %q %v", name, isPlatform)
	}

	// Повторный запуск ничего не меняет.
	again, err := e.svc.EnsurePlatformOrganization(ctx, in)
	if err != nil || again != id {
		t.Fatalf("second run: %s %v", again, err)
	}
	var audits int
	_ = e.d.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE object_id = $1`, id.String()).Scan(&audits)
	if audits != 1 {
		t.Fatalf("%d audit records after repeat, want 1", audits)
	}

	// Новое наименование обновляется.
	in.Name = "ИП Платформа Плюс"
	if _, err := e.svc.EnsurePlatformOrganization(ctx, in); err != nil {
		t.Fatal(err)
	}
	_ = e.d.Pool.QueryRow(ctx, `SELECT name FROM organization WHERE id = $1`, id).Scan(&name)
	if name != in.Name {
		t.Fatalf("name %q after rename", name)
	}

	// Другой ИНН — ошибка запуска.
	other := in
	other.INN = accounttest.INN(domain.SoleProprietor)
	if _, err := e.svc.EnsurePlatformOrganization(ctx, other); !errors.Is(err, app.ErrPlatformConflict) {
		t.Fatalf("other inn: %v", err)
	}
}

func TestEnsurePlatformOrganizationINNTaken(t *testing.T) {
	e := newEnv(t)
	p := e.principal(e.login("owner@example.test"))
	in := accounttest.AdvertiserLegalEntity()
	if _, err := e.svc.CreateOrganization(e.ctx(), p, in); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.EnsurePlatformOrganization(context.Background(),
		app.PlatformOrg{INN: in.INN, Name: "ООО Платформа", LegalType: domain.LegalEntity})
	if !errors.Is(err, app.ErrPlatformConflict) {
		t.Fatalf("inn of a regular organization: %v", err)
	}
}

type orgListener struct{ calls []uuid.UUID }

func (l *orgListener) OrganizationChanged(_ context.Context, id uuid.UUID) error {
	l.calls = append(l.calls, id)
	return nil
}

func TestOrganizationListener(t *testing.T) {
	e := newEnv(t)
	l := &orgListener{}
	e.svc.SetOrganizationListener(l)
	p := e.principal(e.login("owner@example.test"))
	o, err := e.svc.CreateOrganization(e.ctx(), p, accounttest.AdvertiserLegalEntity())
	if err != nil {
		t.Fatal(err)
	}
	p = e.principal(e.login("owner@example.test"))
	phone := "+79995554433"
	if _, err := e.svc.UpdateOrganization(e.ctx(), p, domain.Fields{Phone: &phone}); err != nil {
		t.Fatal(err)
	}
	if len(l.calls) != 0 {
		t.Fatalf("phone change passed to marking: %v", l.calls)
	}
	name := "ООО «Ромашка и партнёры»"
	if _, err := e.svc.UpdateOrganization(e.ctx(), p, domain.Fields{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if len(l.calls) != 1 || l.calls[0] != o.ID {
		t.Fatalf("name change: listener calls %v", l.calls)
	}
}
