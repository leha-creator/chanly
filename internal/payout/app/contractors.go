package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/payout/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/job"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// ContractorView — состояние исполнителя для кабинета блогера (getRocketworkStatus).
type ContractorView struct {
	NextAction      string
	Required        bool
	Status          domain.ContractorStatus
	OnboardingURL   string
	BlockedReason   string
	StatusCheckedAt *time.Time
}

func viewOf(c domain.Contractor) ContractorView {
	action := "complete_registration"
	switch c.Status {
	case domain.ContractorActive:
		action = "none"
	case domain.ContractorNone:
		action = "invite"
	}
	switch c.BlockedReason {
	case "need_permissions", "selfemployment_unverified", "provider_permissions_unverified":
		action = "grant_fns_permissions"
	case "no_requisites":
		action = "add_requisites"
	case "unknown_readiness", "employment_mismatch", "provider_profile_missing_or_ambiguous":
		action = "contact_support"
	}
	return ContractorView{NextAction: action, Required: true, Status: c.Status, OnboardingURL: c.OnboardingURL, BlockedReason: c.BlockedReason,
		StatusCheckedAt: c.StatusCheckedAt}
}

// Status — состояние онбординга организации: юрлицу Рокет Ворк не нужен (A-36).
func (s *Service) Status(ctx context.Context, orgID uuid.UUID) (ContractorView, error) {
	payee, err := s.Organizations.ForPayout(ctx, orgID)
	if err != nil {
		return ContractorView{}, err
	}
	if !domain.ContractorRequired(payee.LegalType) {
		return ContractorView{NextAction: "none", Required: false, Status: domain.ContractorNone}, nil
	}
	c, err := s.Store.ContractorByOrg(ctx, orgID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return ContractorView{NextAction: "invite", Required: true, Status: domain.ContractorNone}, nil
	}
	if err != nil {
		return ContractorView{}, err
	}
	s.log.DebugContext(ctx, "rocketwork status read", logx.KeyOp, "Payout.Status", logx.KeyOrganizationID, orgID,
		"status", c.Status)
	return viewOf(c), nil
}

// Invite начинает онбординг (S-11). Рокет Ворк в запросе не вызывается (I-13): строка invited
// и задание ensure_contractor; ссылка появится после задания. Повтор — тот же ответ.
func (s *Service) Invite(ctx context.Context, orgID uuid.UUID) (ContractorView, error) {
	const op = "Payout.Invite"
	payee, err := s.Organizations.ForPayout(ctx, orgID)
	if err != nil {
		return ContractorView{}, err
	}
	if !domain.ContractorRequired(payee.LegalType) {
		return ContractorView{}, domain.ErrContractorNotRequired
	}
	if !s.Config.ProviderEnabled {
		return ContractorView{}, domain.ErrProviderDisabled
	}
	var (
		c       domain.Contractor
		created bool
	)
	err = s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, created, err = s.Store.InsertContractor(ctx, domain.NewContractor(ids.New(), orgID, s.Clock.Now()))
		if err != nil {
			return err
		}
		if c.Registered() {
			return nil
		}
		return s.Jobs.Enqueue(ctx, job.Job{Kind: KindEnsureContractor, DedupeKey: EnsureContractorKey(orgID),
			Payload: OrgPayload{OrganizationID: orgID}, MaxAttempts: EnsureAttempts})
	})
	if err != nil {
		return ContractorView{}, err
	}
	if created {
		s.log.InfoContext(ctx, "rocketwork contractor invited", logx.KeyOp, op, logx.KeyOrganizationID, orgID,
			KeyContractorID, c.ID, "legal_type", payee.LegalType)
	} else {
		s.log.DebugContext(ctx, "rocketwork contractor already invited", logx.KeyOp, op, logx.KeyOrganizationID, orgID,
			KeyContractorID, c.ID, "status", c.Status)
	}
	return viewOf(c), nil
}

// Refresh — «Проверить статус» (R-10): опрос исполнителя сейчас, ответ — текущий статус.
func (s *Service) Refresh(ctx context.Context, orgID uuid.UUID) (ContractorView, error) {
	const op = "Payout.Refresh"
	view, err := s.Status(ctx, orgID)
	if err != nil {
		return ContractorView{}, err
	}
	if !view.Required {
		return ContractorView{}, domain.ErrContractorNotRequired
	}
	if view.Status == domain.ContractorNone {
		return view, nil
	}
	if !s.Config.ProviderEnabled {
		return ContractorView{}, domain.ErrProviderDisabled
	}
	c, err := s.Store.ContractorByOrg(ctx, orgID, false)
	if err != nil {
		return ContractorView{}, err
	}
	now := s.Clock.Now()
	j := job.Job{Kind: KindPollContractor, DedupeKey: PollContractorKey(c.ID), Payload: ContractorPayload{ContractorID: c.ID}, RunAt: now,
		MaxAttempts: PollAttempts}
	if !c.Registered() {
		j = job.Job{Kind: KindEnsureContractor, DedupeKey: EnsureContractorKey(orgID), Payload: OrgPayload{OrganizationID: orgID},
			RunAt: now, MaxAttempts: EnsureAttempts}
	}
	if err := s.schedule(ctx, j); err != nil {
		return ContractorView{}, err
	}
	s.log.InfoContext(ctx, "rocketwork status refresh requested", logx.KeyOp, op, logx.KeyOrganizationID, orgID,
		KeyContractorID, c.ID)
	return view, nil
}

// CanAccept — placement/domain.ContractorGate (P-03, S-14): юрлицо — да; иначе исполнитель active.
func (s *Service) CanAccept(ctx context.Context, orgID uuid.UUID) (bool, error) {
	payee, err := s.Organizations.ForPayout(ctx, orgID)
	if err != nil {
		return false, err
	}
	if !domain.ContractorRequired(payee.LegalType) {
		return true, nil
	}
	c, err := s.Store.ContractorByOrg(ctx, orgID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return c.CanAccept(), nil
}

// providerWait переводит «Рокет Ворк выключен» в откладывание задания без траты попытки.
func (s *Service) providerWait(ctx context.Context, op string, err error) error {
	if errors.Is(err, domain.ErrProviderDisabled) {
		s.log.WarnContext(ctx, "rocketwork disabled, job postponed", logx.KeyOp, op, "retry_in", s.Config.DisabledRetry.String())
		return job.Postpone(err, s.Config.DisabledRetry)
	}
	return err
}

// EnsureContractor — задание ensure_contractor: создать исполнителя в Рокет Ворк и получить ссылку.
func (s *Service) EnsureContractor(ctx context.Context, orgID uuid.UUID) error {
	const op = "Payout.EnsureContractor"
	c, err := s.Store.ContractorByOrg(ctx, orgID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return job.Permanent(fmt.Errorf("%s: no contractor of organization %s", op, orgID))
	}
	if err != nil {
		return err
	}
	if s.Config.ProviderScope != nil {
		return s.ensureVerifiedContractor(ctx, c)
	}
	if c.Registered() && c.OnboardingURL != "" {
		return s.schedulePoll(ctx, c)
	}
	payee, err := s.Organizations.ForPayout(ctx, orgID)
	if err != nil {
		return err
	}
	pc, err := s.Provider.EnsureContractor(ctx, domain.ContractorRequest{OrganizationID: orgID, LegalType: payee.LegalType,
		INN: payee.INN, FullName: payee.FullName, Phone: payee.Phone})
	if err != nil {
		if rej, ok := domain.AsRejected(err); ok {
			s.contractorError(ctx, c.ID, rej.Error())
			s.log.ErrorContext(ctx, "rocketwork contractor rejected", logx.KeyOp, op, logx.KeyOrganizationID, orgID,
				"inn", logx.MaskINN(payee.INN), logx.KeyError, rej.Error())
			return job.Permanent(err)
		}
		return s.providerWait(ctx, op, err)
	}
	s.log.InfoContext(ctx, "rocketwork contractor ensured", logx.KeyOp, op, logx.KeyOrganizationID, orgID,
		KeyContractorID, c.ID, KeyExternalID, pc.ExternalID, "inn", logx.MaskINN(payee.INN), "status", pc.Status)
	_, err = s.applyContractor(ctx, c.ID, pc)
	return err
}

// EnsureExhausted — попытки ensure_contractor исчерпаны: ошибка видна администратору в строке.
func (s *Service) EnsureExhausted(ctx context.Context, orgID uuid.UUID, cause error) {
	c, err := s.Store.ContractorByOrg(ctx, orgID, false)
	if err == nil {
		s.contractorError(ctx, c.ID, cause.Error())
	}
	s.log.ErrorContext(ctx, "rocketwork contractor ensure exhausted", logx.KeyOp, "Payout.EnsureExhausted",
		logx.KeyOrganizationID, orgID, logx.KeyError, cause.Error())
}

func (s *Service) contractorError(ctx context.Context, id uuid.UUID, msg string) {
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		c, err := s.Store.ContractorByID(ctx, id, true)
		if err != nil {
			return err
		}
		c.LastError, c.UpdatedAt = truncate(msg, 1000), s.Clock.Now()
		return s.Store.UpdateContractor(ctx, c)
	})
	if err != nil {
		s.log.WarnContext(ctx, "rocketwork contractor error not saved", logx.KeyOp, "Payout.ContractorError", logx.KeyError, err.Error())
	}
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// schedulePoll ставит опрос исполнителя, если его нет: active — через ActiveRecheck, иначе — PollFast.
func (s *Service) schedulePoll(ctx context.Context, c domain.Contractor) error {
	delay := s.Config.PollFast
	if c.Status == domain.ContractorActive {
		delay = s.Config.ActiveRecheck
	}
	return s.Jobs.Enqueue(ctx, job.Job{Kind: KindPollContractor, DedupeKey: PollContractorKey(c.ID),
		Payload: ContractorPayload{ContractorID: c.ID}, RunAt: s.Clock.Now().Add(delay), MaxAttempts: PollAttempts})
}

// PollContractor — задание poll_contractor (R-10): invited и pending — по расписанию опроса
// не дольше ContractorPollMaxAge, active — раз в ActiveRecheck, blocked — опрос прекращается.
func (s *Service) PollContractor(ctx context.Context, contractorID uuid.UUID) error {
	const op = "Payout.PollContractor"
	c, err := s.Store.ContractorByID(ctx, contractorID, false)
	if errors.Is(err, domain.ErrNotFound) {
		return job.Permanent(err)
	}
	if err != nil {
		return err
	}
	if !c.Registered() && s.Config.ProviderScope == nil {
		return s.EnsureContractor(ctx, c.OrganizationID)
	}
	var pc domain.ProviderContractor
	if s.Config.ProviderScope != nil {
		pc, err = s.resolveContractor(ctx, c)
	} else {
		pc, err = s.Provider.GetContractor(ctx, c.ExternalID)
	}
	if err != nil {
		return s.providerWait(ctx, op, err)
	}
	if c, err = s.applyContractor(ctx, c.ID, pc); err != nil {
		return err
	}
	now := s.Clock.Now()
	var next time.Duration
	switch {
	case c.Status == domain.ContractorBlocked:
		s.log.DebugContext(ctx, "rocketwork contractor poll stopped", logx.KeyOp, op, KeyContractorID, c.ID, "status", c.Status)
		return nil
	case c.Status == domain.ContractorActive:
		next = s.Config.ActiveRecheck
	case now.Sub(c.CreatedAt) > s.Config.ContractorPollMaxAge:
		s.log.WarnContext(ctx, "rocketwork contractor poll expired", logx.KeyOp, op, KeyContractorID, c.ID, "status", c.Status,
			logx.KeyOrganizationID, c.OrganizationID)
		return nil
	default:
		next = domain.PollDelay(c.CreatedAt, now, s.Config.PollFast, s.Config.PollSlow)
	}
	s.log.DebugContext(ctx, "rocketwork contractor poll", logx.KeyOp, op, KeyContractorID, c.ID, "status", c.Status,
		"next_run_at", now.Add(next))
	return job.Postpone(fmt.Errorf("contractor %s is %s", c.ID, c.Status), next)
}

// ApplyContractor — свежее состояние исполнителя из вебхука (R-10).
func (s *Service) ApplyContractor(ctx context.Context, externalID string) error {
	const op = "Payout.ApplyContractor"
	c, err := s.Store.ContractorByExternalID(ctx, externalID, false)
	if err != nil {
		return err
	}
	pc, err := s.Provider.GetContractor(ctx, externalID)
	if err != nil {
		return s.providerWait(ctx, op, err)
	}
	_, err = s.applyContractor(ctx, c.ID, pc)
	return err
}

// applyContractor переносит ответ сервиса в строку (в своей транзакции); смена статуса —
// INFO и уведомление блогеру.
func (s *Service) applyContractor(ctx context.Context, id uuid.UUID, pc domain.ProviderContractor) (domain.Contractor, error) {
	const op = "Payout.ApplyContractor"
	var (
		c       domain.Contractor
		from    domain.ContractorStatus
		changed bool
	)
	err := s.Tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		if c, err = s.Store.ContractorByID(ctx, id, true); err != nil {
			return err
		}
		from = c.Status
		changed = c.Apply(pc, s.Clock.Now())
		if err := s.Store.UpdateContractor(ctx, c); err != nil {
			return err
		}
		if c.Status == domain.ContractorBlocked {
			return nil
		}
		return s.schedulePoll(ctx, c)
	})
	if err != nil {
		return domain.Contractor{}, err
	}
	if !changed {
		return c, nil
	}
	s.log.InfoContext(ctx, "rocketwork contractor status changed", logx.KeyOp, op, KeyContractorID, c.ID,
		logx.KeyOrganizationID, c.OrganizationID, "from", from, "to", c.Status, "blocked_reason", c.BlockedReason)
	switch c.Status {
	case domain.ContractorActive:
		s.notify(ctx, domain.Notice{Template: domain.NoticeRocketworkActive, OrganizationID: c.OrganizationID, ObjectID: c.ID})
	case domain.ContractorBlocked:
		s.notify(ctx, domain.Notice{Template: domain.NoticeRocketworkBlocked, OrganizationID: c.OrganizationID, ObjectID: c.ID,
			Data: map[string]any{"reason": c.BlockedReason}})
	}
	return c, nil
}
