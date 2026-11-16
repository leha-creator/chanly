package bridge

import (
	"context"
	"fmt"

	accountpg "github.com/leha-creator/chanly/internal/account/postgres"
	"github.com/leha-creator/chanly/internal/payout/domain"
	placementpg "github.com/leha-creator/chanly/internal/placement/postgres"
)

type PayoutActSource struct {
	Organizations *accountpg.Organizations
	Placements    *placementpg.Placements
}

func (s PayoutActSource) ForAct(ctx context.Context, p domain.Payout) (domain.ActData, error) {
	org, err := s.Organizations.ForPayout(ctx, p.BloggerOrganizationID)
	if err != nil {
		return domain.ActData{}, err
	}
	accepted, err := s.Organizations.BloggerOfferAcceptedAt(ctx, p.BloggerOrganizationID, p.CreatedAt)
	if err != nil {
		return domain.ActData{}, err
	}
	pl, err := s.Placements.ForPayoutAct(ctx, p.PlacementID)
	if err != nil {
		return domain.ActData{}, err
	}
	if pl.BloggerOrgID != p.BloggerOrganizationID || pl.PublishedAt == nil || pl.RemovedAt == nil {
		return domain.ActData{}, fmt.Errorf("%w: act publication incomplete", domain.ErrInvalidPayout)
	}
	return domain.ActData{OrganizationID: org.ID, LegalType: domain.LegalType(org.LegalType), TaxRegime: org.TaxRegime,
		OfferAcceptedAt: accepted, OrderNumber: pl.OrderNumber, PublicationURL: pl.PublicationURL,
		TopHours: pl.TopHours, DurationHours: pl.DurationHours, PublishedAt: *pl.PublishedAt, RemovedAt: *pl.RemovedAt}, nil
}
