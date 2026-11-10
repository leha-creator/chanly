package app

import (
	"context"
	"fmt"

	"github.com/leha-creator/chanly/internal/payout/domain"
)

func (s *Service) preparePayoutWorkflow(ctx context.Context, p domain.Payout) (domain.Workflow, error) {
	if s.Config.ProviderScope == nil || s.Config.AgreementTemplate == nil {
		return domain.Workflow{}, fmt.Errorf("%w: agreement policy missing", domain.ErrInvalidPayout)
	}
	store, ok := s.Store.(domain.WorkflowStore)
	if !ok {
		return domain.Workflow{}, domain.ErrProviderDisabled
	}
	b, err := store.ExecutorBinding(ctx, *s.Config.ProviderScope, p.BloggerOrganizationID)
	if err != nil {
		return domain.Workflow{}, err
	}
	terms := *s.Config.AgreementTemplate
	terms.Amount = p.Amount
	terms.ExecutorID = b.ExecutorID
	terms.EmploymentKind = b.EmploymentKind
	if _, _, err := terms.Snapshot(); err != nil {
		return domain.Workflow{}, err
	}
	return store.PrepareWorkflow(ctx, domain.Workflow{PayoutID: p.ID, Scope: *s.Config.ProviderScope, Terms: terms, CreatedAt: s.Clock.Now(), UpdatedAt: s.Clock.Now()})
}
