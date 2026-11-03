package app

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/admin/domain"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// AuditFilter — отбор журнала действий (R-15). ManualOnly — только действия администраторов.
type AuditFilter struct {
	ActorUserID    *uuid.UUID
	ActorType      *string
	Action         *string
	ObjectType     *string
	ObjectID       *string
	OrganizationID *uuid.UUID
	From, To       *time.Time
	ManualOnly     bool
}

// AuditEntry — запись журнала без IP-адреса; data — как записано (маскирование при записи).
type AuditEntry struct {
	ID             uuid.UUID
	ActorType      string
	ActorUserID    *uuid.UUID
	OrganizationID *uuid.UUID
	Action         string
	ObjectType     string
	ObjectID       string
	Reason         *string
	Data           json.RawMessage
	CreatedAt      time.Time
}

// AuditReader — чтение audit_log (admin/postgres).
type AuditReader interface {
	AuditLog(ctx context.Context, f AuditFilter, p domain.Page) ([]AuditEntry, int, error)
}

// AuditLog — журнал действий для экрана adm/audit, новые первыми.
func (s *Service) AuditLog(ctx context.Context, f AuditFilter, p domain.Page) ([]AuditEntry, int, error) {
	if f.ManualOnly {
		admin := "admin"
		f.ActorType = &admin
	}
	items, total, err := s.Audits.AuditLog(ctx, f, p)
	if err != nil {
		return nil, 0, err
	}
	s.log.DebugContext(ctx, "audit log listed", logx.KeyOp, "Admin.AuditLog", "count", len(items), "total", total)
	return items, total, nil
}
