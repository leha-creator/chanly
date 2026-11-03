package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// AdminRoleResult — итог выдачи или снятия роли администратора.
type AdminRoleResult struct {
	UserID uuid.UUID
	// Created — пользователя не было, он создан.
	Created bool
	// Changed — роль изменилась; повтор той же команды ничего не меняет.
	Changed bool
}

// SetAdminRole выдаёт роль администратора пользователю с почтой email или снимает её
// (role = nil). Пользователя без учётной записи создаёт: он войдёт по коду как обычно.
// Команда maxim admin grant до API администратора (R-2 этапа «Каталог»); действие пишется
// в журнал действий с актором system, почта — только в маске.
func (s *Service) SetAdminRole(ctx context.Context, email string, role *domain.AdminRole) (AdminRoleResult, error) {
	const op = "Account.SetAdminRole"
	if role != nil && *role != domain.AdminModerator && *role != domain.AdminSenior {
		return AdminRoleResult{}, apperr.Validation("role", "unknown")
	}
	email = domain.NormalizeEmail(email)
	if email == "" {
		return AdminRoleResult{}, apperr.Validation("email", "required")
	}
	var res AdminRoleResult
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		now := s.Clock.Now()
		u, err := s.Users.GetByEmail(ctx, email)
		if errors.Is(err, domain.ErrNotFound) {
			if role == nil {
				return nil // снимать нечего
			}
			u = domain.NewUser(ids.New(), email, now)
			if err := s.Users.Create(ctx, u); err != nil {
				return err
			}
			res.Created = true
		} else if err != nil {
			return err
		}
		res.UserID = u.ID
		if sameRole(u.AdminRole, role) && !res.Created {
			return nil
		}
		if err := s.Users.SetAdminRole(ctx, u.ID, role, now); err != nil {
			return err
		}
		res.Changed = true
		before, after := roleName(u.AdminRole), roleName(role)
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "user.admin_role_changed", ObjectType: "user", ObjectID: u.ID.String(),
			Data:      audit.Mask(map[string]any{"email": email, "before": before, "after": after, "created": res.Created}),
			ActorType: audit.ActorSystem,
		}); err != nil {
			return err
		}
		s.log.InfoContext(ctx, "admin role changed", logx.KeyOp, op, logx.KeyUserID, u.ID, "role", after, "created", res.Created)
		return nil
	})
	return res, err
}

func sameRole(a, b *domain.AdminRole) bool {
	return roleName(a) == roleName(b)
}

func roleName(r *domain.AdminRole) string {
	if r == nil {
		return ""
	}
	return string(*r)
}
