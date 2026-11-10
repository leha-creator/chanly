package app

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/leha-creator/chanly/internal/payout/domain"

	"github.com/leha-creator/chanly/internal/platform/apperr"
	"github.com/leha-creator/chanly/internal/platform/audit"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// Действия журнала кабинета блогера: по ним считается лимит частоты (07 §4).
const (
	ActionRocketworkInvite  = "rocketwork.invite"
	ActionRocketworkRefresh = "rocketwork.refresh"
	// CabinetRequestsPerHour — приглашений и проверок статуса пользователя в час.
	CabinetRequestsPerHour = 10
)

// Limits — счётчики действий для лимитов частоты, общие для всех экземпляров api (07 §4).
type Limits interface {
	CountByUserSince(ctx context.Context, action string, userID uuid.UUID, since time.Time) (int, error)
}

// InviteFromCabinet — inviteRocketwork: лимит частоты, Invite и запись в журнал действий.
func (s *Service) InviteFromCabinet(ctx context.Context, orgID, userID uuid.UUID) (ContractorView, error) {
	const op = "Payout.InviteFromCabinet"
	if err := s.cabinetLimit(ctx, op, ActionRocketworkInvite, userID); err != nil {
		return ContractorView{}, err
	}
	v, err := s.Invite(ctx, orgID)
	if err != nil {
		return ContractorView{}, err
	}
	s.log.InfoContext(ctx, "rocketwork invite requested", logx.KeyOp, op, logx.KeyOrganizationID, orgID, "status", v.Status)
	return v, s.cabinetAudit(ctx, ActionRocketworkInvite, orgID)
}

// RefreshFromCabinet — refreshRocketworkStatus: лимит частоты, Refresh и запись в журнал.
func (s *Service) RefreshFromCabinet(ctx context.Context, orgID, userID uuid.UUID) (ContractorView, error) {
	const op = "Payout.RefreshFromCabinet"
	if err := s.cabinetLimit(ctx, op, ActionRocketworkRefresh, userID); err != nil {
		return ContractorView{}, err
	}
	v, err := s.Refresh(ctx, orgID)
	if err != nil {
		return ContractorView{}, err
	}
	return v, s.cabinetAudit(ctx, ActionRocketworkRefresh, orgID)
}

func (s *Service) cabinetLimit(ctx context.Context, op, action string, userID uuid.UUID) error {
	if s.Limits == nil || userID == uuid.Nil {
		return nil
	}
	n, err := s.Limits.CountByUserSince(ctx, action, userID, s.Clock.Now().Add(-time.Hour))
	if err != nil {
		return err
	}
	if n >= CabinetRequestsPerHour {
		s.log.WarnContext(ctx, "rate limited", logx.KeyOp, op, "user_id", userID, "action", action)
		return apperr.RateLimited("user", time.Hour)
	}
	return nil
}

func (s *Service) cabinetAudit(ctx context.Context, action string, orgID uuid.UUID) error {
	if s.Audit == nil {
		return nil
	}
	return s.Audit.Record(ctx, audit.Entry{Action: action, ObjectType: "organization", ObjectID: orgID.String(),
		ActorType: audit.ActorBlogger, OrganizationID: &orgID})
}

func (s *Service) OnboardingFromCabinet(ctx context.Context, orgID, userID uuid.UUID, action string, names domain.RegistrationNames) (ContractorView, error) {
	if action != "invite" && action != "register" && action != "update" && action != "rebind" {
		return ContractorView{}, domain.ErrInvalidPayout
	}
	if err := s.cabinetLimit(ctx, "Payout.OnboardingFromCabinet", "rocketwork.onboarding", userID); err != nil {
		return ContractorView{}, err
	}
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.RequestOnboarding(ctx, orgID, action, names); err != nil {
			return err
		}
		return s.cabinetAudit(ctx, "rocketwork.onboarding", orgID)
	})
	if err != nil {
		return ContractorView{}, err
	}
	return s.Status(ctx, orgID)
}
