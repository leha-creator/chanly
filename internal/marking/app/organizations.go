package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
	"github.com/leha-creator/chanly/internal/platform/logx"
)

// buildOrganization — запрос контрагента (R-5, R-6). Зависимостей нет.
func (s *Service) buildOrganization(ctx context.Context, r *orgRecord) (*outgoing, error, error) {
	req, hash, err := s.organizationRequest(ctx, r.v)
	if err != nil {
		return nil, nil, err
	}
	return &outgoing{
		ref: r.ref(), externalID: r.externalID(), hash: hash,
		call: func(ctx context.Context, api domain.OrdAPI) (domain.Receipt, error) {
			return api.UpsertOrganization(ctx, req)
		},
	}, nil, nil
}

func (s *Service) organizationRequest(ctx context.Context, o domain.Organization) (domain.OrganizationRequest, string, error) {
	details, err := s.Accounts.Details(ctx, o.OrganizationID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.OrganizationRequest{}, "", errors.Join(domain.ErrPermanent, err)
	}
	if err != nil {
		return domain.OrganizationRequest{}, "", err
	}
	req, err := domain.NewOrganizationRequest(details, o.IsRr)
	if err != nil {
		return domain.OrganizationRequest{}, "", err
	}
	hash, err := domain.PayloadHash(req)
	return req, hash, err
}

// OrganizationChanged — реквизиты организации изменились (R-15, порт account/domain.OrganizationListener):
// новая нагрузка — контрагент снова pending и задание отправки, в том числе из error. Работает в
// транзакции правки; организация, ещё не зарегистрированная в ОРД, пропускается.
func (s *Service) OrganizationChanged(ctx context.Context, orgID uuid.UUID) error {
	const op = "Marking.OrganizationChanged"
	return s.Tx.InTx(ctx, func(ctx context.Context) error {
		existing, err := s.Organizations.ByOrganization(ctx, orgID)
		if errors.Is(err, domain.ErrNotFound) {
			s.log.DebugContext(ctx, "organization not registered in ord", logx.KeyOp, op, logx.KeyOrganizationID, orgID)
			return nil
		}
		if err != nil {
			return err
		}
		o, err := s.Organizations.GetForUpdate(ctx, existing.ID)
		if err != nil {
			return err
		}
		_, hash, err := s.organizationRequest(ctx, o)
		if _, ok := domain.BuildFailureReason(err); ok {
			// Ошибку сборки (например, слишком длинное наименование) зафиксирует задание.
			hash = ""
		} else if err != nil {
			return err
		}
		if !o.Sync.Requeue(hash) && !(hash == "" && o.Sync.Invalidate()) {
			s.log.DebugContext(ctx, "ord organization unchanged", logx.KeyOp, op, logx.KeyOrganizationID, orgID)
			return nil
		}
		o.UpdatedAt = s.Clock.Now()
		if err := s.Organizations.Save(ctx, o); err != nil {
			return err
		}
		return s.enqueueSync(ctx, op, o.Ref(), reasonChanged)
	})
}

// ensureOrganization — контрагент организации: нет записи — pending и задание отправки. isRr —
// организация-блогер (R-6): признак только включается; у существующей записи — новая отправка.
func (s *Service) ensureOrganization(ctx context.Context, orgID uuid.UUID, isRr bool) (domain.Organization, error) {
	const op = "Marking.EnsureOrganization"
	o, err := s.Organizations.ByOrganization(ctx, orgID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		now := s.Clock.Now()
		rec, created, err := s.Organizations.Insert(ctx, domain.Organization{
			ID: ids.New(), OrganizationID: orgID, IsRr: isRr, Sync: domain.NewSyncState(), CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return domain.Organization{}, err
		}
		if created {
			if err := s.enqueueSync(ctx, op, rec.Ref(), reasonDependant); err != nil {
				return domain.Organization{}, err
			}
		}
		if !isRr || rec.IsRr {
			return rec, nil
		}
		o = rec
	case err != nil:
		return domain.Organization{}, err
	case !isRr || o.IsRr:
		return o, nil
	}
	// Организация стала блогером: isRr = true, контрагент отправляется заново.
	o, err = s.Organizations.GetForUpdate(ctx, o.ID)
	if err != nil {
		return domain.Organization{}, err
	}
	if o.IsRr {
		return o, nil
	}
	o.IsRr = true
	o.Sync.Invalidate()
	o.UpdatedAt = s.Clock.Now()
	if err := s.Organizations.Save(ctx, o); err != nil {
		return domain.Organization{}, err
	}
	return o, s.enqueueSync(ctx, op, o.Ref(), reasonChanged)
}

// platformOrganization — контрагент платформы (R-5): строку организации обеспечивает запуск.
func (s *Service) platformOrganization(ctx context.Context) (domain.Organization, error) {
	p, err := s.Accounts.Platform(ctx)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.Organization{}, errors.Join(domain.ErrPermanent, errors.New("platform organization is not configured"))
	}
	if err != nil {
		return domain.Organization{}, err
	}
	return s.ensureOrganization(ctx, p.ID, false)
}

func dependency(need domain.Requirement, ref domain.ObjectRef, st domain.SyncState) domain.Dependency {
	return domain.Dependency{Need: need, Exists: true, Ref: ref, State: st}
}
