package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/leha-creator/chanly/internal/marking/domain"
	"github.com/leha-creator/chanly/internal/platform/ids"
)

// buildContract — запрос договора (R-7): стороны готовы (R-4), дата — первое принятие оферты.
func (s *Service) buildContract(ctx context.Context, r *contractRecord) (*outgoing, error, error) {
	const op = "Marking.Sync"
	c := r.v
	platform, err := s.platformOrganization(ctx)
	if err != nil {
		return nil, nil, err
	}
	otherID, need := c.ClientOrganizationID, domain.NeedAdvertiserOrganization
	if c.Kind == domain.ContractPublisher {
		otherID, need = c.ContractorOrganizationID, domain.NeedBloggerOrganization
	}
	other, err := s.ensureOrganization(ctx, otherID, c.Kind == domain.ContractPublisher)
	if err != nil {
		return nil, nil, err
	}
	v := domain.DependencyVerdict([]domain.Dependency{
		dependency(domain.NeedPlatformOrganization, platform.Ref(), platform.Sync),
		dependency(need, other.Ref(), other.Sync),
	})
	if !v.Ready() {
		return nil, s.wait(ctx, op, r.ref(), v), nil
	}
	acc, ok, err := s.Accounts.FirstOfferAcceptance(ctx, c.OfferOrganization(), domain.OfferKind(c.Kind))
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, domain.ErrOfferNotAccepted
	}
	r.v.SignedDate = domain.ContractDate(acc.AcceptedAt)
	r.v.Number = domain.ContractNumber(s.Config.Contracts.NumberPrefix, acc.Version)
	req := domain.NewContractRequest(r.v, s.Config.Contracts.Params(c.Kind))
	hash, err := domain.PayloadHash(req)
	if err != nil {
		return nil, nil, err
	}
	return &outgoing{
		ref: r.ref(), externalID: r.externalID(), hash: hash,
		call: func(ctx context.Context, api domain.OrdAPI) (domain.Receipt, error) {
			return api.UpsertContract(ctx, req)
		},
	}, nil, nil
}

// ensureContract — договор вида с организацией other (рекламодатель или блогер): нет записи —
// pending и задание отправки. Стороны (A-31) — по виду.
func (s *Service) ensureContract(ctx context.Context, kind domain.ContractKind, platformOrg, other uuid.UUID) (domain.Contract, error) {
	const op = "Marking.EnsureContract"
	client, contractor := domain.ContractParties(kind, platformOrg, other)
	c, err := s.Contracts.ByParties(ctx, kind, client, contractor)
	if !errors.Is(err, domain.ErrNotFound) {
		return c, err
	}
	now := s.Clock.Now()
	rec, created, err := s.Contracts.Insert(ctx, domain.Contract{
		ID: ids.New(), Kind: kind, ClientOrganizationID: client, ContractorOrganizationID: contractor,
		Sync: domain.NewSyncState(), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return domain.Contract{}, err
	}
	if created {
		if err := s.enqueueSync(ctx, op, rec.Ref(), reasonDependant); err != nil {
			return domain.Contract{}, err
		}
	}
	return rec, nil
}

// EnsureInitialChain — контрагенты платформы и рекламодателя и изначальный договор (R-4) в
// транзакции вызывающего: недостающие записи — pending с заданиями.
func (s *Service) EnsureInitialChain(ctx context.Context, advertiserOrg uuid.UUID) (domain.Contract, error) {
	platform, err := s.platformOrganization(ctx)
	if err != nil {
		return domain.Contract{}, err
	}
	if _, err := s.ensureOrganization(ctx, advertiserOrg, false); err != nil {
		return domain.Contract{}, err
	}
	return s.ensureContract(ctx, domain.ContractInitial, platform.OrganizationID, advertiserOrg)
}

// EnsurePublisherChain — контрагенты платформы и блогера (isRr) и договор с блогером (R-4, R-13).
func (s *Service) EnsurePublisherChain(ctx context.Context, bloggerOrg uuid.UUID) (domain.Contract, error) {
	platform, err := s.platformOrganization(ctx)
	if err != nil {
		return domain.Contract{}, err
	}
	if _, err := s.ensureOrganization(ctx, bloggerOrg, true); err != nil {
		return domain.Contract{}, err
	}
	return s.ensureContract(ctx, domain.ContractPublisher, platform.OrganizationID, bloggerOrg)
}
