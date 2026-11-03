package app_test

import (
	"testing"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestDevEnsureUser(t *testing.T) {
	e := newEnv(t)
	u, created, err := e.svc.DevEnsureUser(e.ctx(), "  Adv@Example.TEST ")
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	if u.Email != "adv@example.test" {
		t.Fatalf("email = %q", u.Email)
	}
	again, created, err := e.svc.DevEnsureUser(e.ctx(), "adv@example.test")
	if err != nil || created || again.ID != u.ID {
		t.Fatalf("repeat: id=%v created=%v err=%v", again.ID, created, err)
	}
	// Пользователь входит по коду как обычно.
	if r := e.login("adv@example.test"); r.Principal.UserID != u.ID {
		t.Fatalf("login user = %v, want %v", r.Principal.UserID, u.ID)
	}
	if _, _, err := e.svc.DevEnsureUser(e.ctx(), " "); !apperr.Is(err, apperr.CodeValidationFailed) {
		t.Fatalf("blank email: %v", err)
	}
}
