package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/account/domain"
	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Организации и пользователи для администратора (этап «API администратора», R-12 … R-14).
// Маскирование ИНН, почты и телефона по роли — в httpapi; здесь — данные целиком.

// AdminOrganizationDetail — карточка организации для администратора.
type AdminOrganizationDetail struct {
	Organization *domain.Organization
	Members      []domain.User
}

// AdminOrganizations — поиск организаций по ИНН, наименованию или почте участника.
func (s *Service) AdminOrganizations(ctx context.Context, f domain.AdminOrganizationFilter) ([]domain.AdminOrganizationItem, int, error) {
	const op = "Account.AdminOrganizations"
	if s.Admin == nil {
		return nil, 0, errors.New("account: admin organizations are not wired")
	}
	f.Query = strings.TrimSpace(f.Query)
	if f.Cabinet != "" && f.Cabinet != domain.CabinetAdvertiser && f.Cabinet != domain.CabinetBlogger {
		return nil, 0, apperr.Validation("role", "invalid")
	}
	items, total, err := s.Admin.AdminList(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	s.log.DebugContext(ctx, "admin organizations listed", logx.KeyOp, op, "count", len(items), "total", total)
	return items, total, nil
}

// AdminOrganization — реквизиты и участники организации.
func (s *Service) AdminOrganization(ctx context.Context, id uuid.UUID) (*AdminOrganizationDetail, error) {
	if s.Admin == nil {
		return nil, errors.New("account: admin organizations are not wired")
	}
	o, err := s.Admin.AdminGet(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return nil, apperr.NotFound("organization not found")
	}
	if err != nil {
		return nil, err
	}
	members, err := s.Admin.Members(ctx, id)
	if err != nil {
		return nil, err
	}
	return &AdminOrganizationDetail{Organization: o, Members: members}, nil
}

// AdminUpdateOrganization исправляет наименование и КПП (S-20). Проверки — те же, что в кабинете;
// изменение наименования или КПП отправляет контрагента в ОРД заново (R-15 этапа «Маркировка»).
func (s *Service) AdminUpdateOrganization(ctx context.Context, adminID, id uuid.UUID, name, kpp *string, reason string) (*AdminOrganizationDetail, error) {
	const op = "Account.AdminUpdateOrganization"
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, apperr.Validation("reason", "required")
	}
	if s.Admin == nil {
		return nil, errors.New("account: admin organizations are not wired")
	}
	var changed []string
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.Admin.AdminGet(ctx, id); err != nil {
			if errors.Is(err, domain.ErrNotFound) {
				return apperr.NotFound("organization not found")
			}
			return err
		}
		o, err := s.Organizations.GetForUpdate(ctx, id)
		if err != nil {
			return err
		}
		before := o.Values()
		if changed, err = o.Update(domain.Fields{Name: name, KPP: kpp}, s.Clock.Now()); err != nil {
			return err
		}
		if len(changed) == 0 {
			return nil
		}
		if err := s.Organizations.Update(ctx, o); err != nil {
			return err
		}
		if err := s.Audit.Record(ctx, audit.Entry{
			Action: "organization.admin_updated", ObjectType: "organization", ObjectID: o.ID.String(),
			Reason: &reason, ActorType: audit.ActorAdmin, OrganizationID: &o.ID, Data: audit.Diff(before, o.Values(), changed),
		}); err != nil {
			return err
		}
		if slices.Contains(changed, "name") || slices.Contains(changed, "kpp") {
			return s.organizationListener().OrganizationChanged(ctx, o.ID)
		}
		return nil
	})
	if err != nil {
		s.logRejected(ctx, op, err)
		return nil, err
	}
	if len(changed) > 0 {
		s.log.WarnContext(ctx, "admin organization updated", logx.KeyOp, op, logx.KeyOrganizationID, id,
			logx.KeyUserID, adminID, "fields", changed)
	}
	return s.AdminOrganization(ctx, id)
}

// BlockUser блокирует пользователя: вход невозможен, действующие сессии завершаются (R-12).
// Администратора — 409 admin_user.
func (s *Service) BlockUser(ctx context.Context, adminID, userID uuid.UUID, reason string) (*domain.User, error) {
	return s.setBlocked(ctx, "Account.BlockUser", adminID, userID, reason, true)
}

// UnblockUser снимает блокировку.
func (s *Service) UnblockUser(ctx context.Context, adminID, userID uuid.UUID, reason string) (*domain.User, error) {
	return s.setBlocked(ctx, "Account.UnblockUser", adminID, userID, reason, false)
}

func (s *Service) setBlocked(ctx context.Context, op string, adminID, userID uuid.UUID, reason string, block bool) (*domain.User, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, apperr.Validation("reason", "required")
	}
	var (
		out     *domain.User
		changed bool
		revoked int64
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		u, err := s.Users.GetForUpdate(ctx, userID)
		if errors.Is(err, domain.ErrNotFound) {
			return apperr.NotFound("user not found")
		}
		if err != nil {
			return err
		}
		now := s.Clock.Now()
		if block {
			if changed, err = u.Block(reason, now); err != nil {
				return err
			}
		} else {
			if u.AnonymizedAt != nil {
				// Обезличенную учётную запись не вернуть: отказ, а не «успех» без изменений (R-26).
				return apperr.Conflict("user is anonymized").WithDetail("reason", "anonymized")
			}
			changed = u.Unblock(now)
		}
		out = u
		if !changed {
			return nil
		}
		if err := s.Users.SetBlocked(ctx, u); err != nil {
			return err
		}
		if block {
			if revoked, err = s.Sessions.RevokeAllForUser(ctx, u.ID, now); err != nil {
				return err
			}
		}
		action := "user.unblocked"
		if block {
			action = "user.blocked"
		}
		return s.Audit.Record(ctx, audit.Entry{
			Action: action, ObjectType: "user", ObjectID: u.ID.String(), Reason: &reason, ActorType: audit.ActorAdmin,
			Data: map[string]any{"sessions_revoked": revoked},
		})
	})
	if err != nil {
		s.logRejected(ctx, op, err)
		return nil, err
	}
	if changed {
		msg := "user unblocked"
		if block {
			msg = "user blocked"
		}
		s.log.WarnContext(ctx, msg, logx.KeyOp, op, logx.KeyUserID, userID, "admin_id", adminID, "sessions_revoked", revoked)
	}
	return out, nil
}
