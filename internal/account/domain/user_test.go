package domain

import (
	"testing"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/platform/apperr"
)

func TestUserBlock(t *testing.T) {
	u := NewUser(uuid.New(), "a@example.ru", now)
	if changed, err := u.Block("fraud", now); err != nil || !changed || !u.Blocked() || u.BlockedAt == nil || *u.BlockedReason != "fraud" {
		t.Fatalf("block: %v %v %+v", changed, err, u)
	}
	if changed, err := u.Block("again", now); err != nil || changed || *u.BlockedReason != "fraud" {
		t.Fatalf("repeat block: %v %v", changed, err)
	}
	if !u.Unblock(now) || u.Blocked() || u.BlockedAt != nil || u.BlockedReason != nil {
		t.Fatalf("unblock: %+v", u)
	}
	if u.Unblock(now) {
		t.Fatal("repeat unblock changed")
	}
	role := AdminModerator
	u.AdminRole = &role
	_, err := u.Block("x", now)
	if e, ok := apperr.As(err); !ok || e.Code != apperr.CodeConflict || e.Details["reason"] != ReasonAdminUser {
		t.Fatalf("block admin: %v", err)
	}
}

func TestUserAnonymize(t *testing.T) {
	u := NewUser(uuid.New(), "subject@example.ru", now)
	if changed, err := u.Anonymize(now); err != nil || !changed || !u.Blocked() || u.AnonymizedAt == nil ||
		u.Email != AnonymizedEmail(u.ID) || *u.BlockedReason != ReasonPersonalDataRequest {
		t.Fatalf("anonymize: %v %v %+v", changed, err, u)
	}
	if changed, err := u.Anonymize(now); err != nil || changed {
		t.Fatalf("repeat anonymize: %v %v", changed, err)
	}
	if u.Unblock(now) || !u.Blocked() {
		t.Fatal("anonymized user unblocked")
	}
	// Заблокированный раньше сохраняет основание блокировки.
	b := NewUser(uuid.New(), "b@example.ru", now)
	_, _ = b.Block("fraud", now)
	if _, err := b.Anonymize(now); err != nil || *b.BlockedReason != "fraud" {
		t.Fatalf("anonymize blocked: %v %+v", err, b)
	}
	role := AdminSenior
	a := NewUser(uuid.New(), "adm@example.ru", now)
	a.AdminRole = &role
	if _, err := a.Anonymize(now); err == nil || a.AnonymizedAt != nil {
		t.Fatalf("anonymize admin: %v", err)
	}
}
